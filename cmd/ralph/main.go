// Command ralph drives an iterative build agent (the "ralph loop")
// against a project's requirements directory.
//
// Run `ralph help` for a full operator manual. The minimal invocation
// is plain `ralph`, run from the project root of a tree scaffolded by
// `ralph init` (the directory that contains reqs/ and app-root/).
// ralph then spawns the agent with its working directory set to
// app-root/, so the agent's standing AGENTS.md auto-loads.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
	"github.com/ai4mgreenly/ralph-loops/internal/loop"
	"github.com/ai4mgreenly/ralph-loops/internal/reqs"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// version is the build identifier reported by `ralph version` and
// stamped into the run banner. It is overridden at link time via
// `-ldflags "-X main.version=..."` (see the Makefile); the default
// value here is what unstamped builds (e.g. `go run`) report.
var version = "dev"

// kickoffPrompt is the single-iteration nudge sent to the agent. The
// standing operator instructions live in the app-root AGENTS.md file
// scaffolded by `ralph init`; claude auto-loads that file from the
// working directory, so the kickoff only needs to wake the agent and
// point it at the standing instructions.
const kickoffPrompt = "Read AGENTS.md if you have not already, then perform one iteration of work as described there."

// Default values for every flag the loop subcommand accepts. Centralised
// here so the help text and the FlagSet stay in sync.
//
// defaultReqs is project-root-relative because the loop subcommand is
// invoked from the project root. defaultUnverifiedReqs is different
// because `ralph unverified` is called by the agent from inside the
// app-root subdirectory, where the spec sits at "../reqs".
const (
	defaultReqs            = "reqs"
	defaultAppRoot         = "app-root"
	defaultUnverifiedReqs  = "../reqs"
	defaultEngine          = "claude"
	defaultModel           = "sonnet"
	defaultEffort          = "high"
	defaultConfigDir       = ""
	defaultTools           = ""
	defaultOneMContext     = true
	defaultClaudeAIMCP     = false
	defaultSkipPermissions = true
	defaultOutputLines     = 10
)

// defaultDuration is the wall-clock cap when --duration is not given.
// Zero means unlimited.
const defaultDuration time.Duration = 0

// Exit codes follow the convention used by Unix CLIs: 0 success, 1
// runtime error, 2 usage error.
const (
	exitSuccess = 0
	exitRuntime = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main's body in a testable shape: arguments come in, output
// goes to the supplied writers, and the exit status is returned rather
// than imposed via os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return exitUsage
	}
	// Real subcommands take precedence over the loop's flag parser so
	// that e.g. `ralph init --version` doesn't get hijacked into a
	// version print. The bare-word `version`/`help` shortcuts also live
	// here; the matching --version/--help flags are handled inside
	// runLoop after flag parsing so they work regardless of position.
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "newid":
		return runNewID(args[1:], stdout, stderr)
	case "time-of":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "ralph time-of: requires exactly one ID argument")
			return exitUsage
		}
		t, err := idgen.TimeOf(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "ralph: %s\n", err)
			return exitUsage
		}
		fmt.Fprintln(stdout, t.UTC().Format("2006-01-02T15:04:05.000Z"))
		return exitSuccess
	case "unverified":
		return runUnverified(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "ralph %s\n", version)
		return exitSuccess
	case "help":
		writeUsagePaged(stdout)
		return exitSuccess
	default:
		return runLoop(args, stdout, stderr)
	}
}

// unverifiedReport is the structured result printed by the
// `ralph unverified` subcommand. The status field disambiguates the
// done state so an agent reading the output can tell "all verified"
// from "command produced no output at all" — empty stdout, with a
// shell pipeline that swallowed an error, is too easy to misread.
type unverifiedReport struct {
	// Status is "done" when every spec ID is already verified, and
	// "pending" otherwise. The two strings are the only possible
	// values.
	Status string `json:"status"`
	// Count is the length of List, repeated for callers that prefer
	// to branch on a number rather than parse the array.
	Count int `json:"count"`
	// List is the sorted set of unverified IDs. Serialised as `[]`
	// (never `null`) when empty so JSON consumers can rely on a
	// single shape.
	List []string `json:"list"`
}

// runNewID mints -n requirement IDs and prints them to stdout, one
// per line. Each ID is anchored to a millisecond that has already
// elapsed by the time it is minted, so the ID space stays reserved
// for past wall-clock instants — never the future. Because [idgen]
// derives one ID per millisecond, minting N distinct IDs takes at
// least ~N-1 ms of wall clock: when the current millisecond matches
// the one used for the previous mint, the loop sleeps until the next
// tick rather than skipping ahead.
func runNewID(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph newid", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var count int
	fs.IntVar(&count, "number", 1, "number of IDs to mint")
	fs.IntVar(&count, "n", 1, "number of IDs to mint (shorthand)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "ralph newid: takes no positional arguments")
		return exitUsage
	}
	if count <= 0 {
		fmt.Fprintf(stderr, "ralph newid: --number must be > 0, got %d\n", count)
		return exitUsage
	}

	var lastMs int64 = -1
	for i := 0; i < count; i++ {
		// Spin until time.Now() has crossed into a millisecond strictly
		// later than the last one we minted from. The first iteration's
		// gate (lastMs == -1) admits any non-negative ms, so a single
		// mint costs no wall-clock wait.
		var now time.Time
		for {
			now = time.Now()
			ms := now.Sub(idgen.Epoch).Milliseconds()
			if ms > lastMs {
				lastMs = ms
				break
			}
			time.Sleep(time.Millisecond)
		}
		fmt.Fprintln(stdout, idgen.NewAt(now))
	}
	return exitSuccess
}

// runUnverified emits a single-line JSON [unverifiedReport] describing
// the IDs that appear in the spec under --reqs but are not yet
// recorded in the local .ralph/requirements-verified.jsonl ledger. It
// is the single-tool-call replacement for the prompt's grep + jsonl +
// diff procedure: faster, deterministic, cheap on the agent's context
// budget, and — because the report is always JSON — never produces
// ambiguous empty output that an agent would feel compelled to retry.
//
// The ledger is read from the process's current working directory, so
// the command does the right thing when invoked from the app-root the
// agent itself runs in.
func runUnverified(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph unverified", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reqsDir := fs.String("reqs", defaultUnverifiedReqs, "path to requirements directory")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "ralph unverified: takes no positional arguments")
		return exitUsage
	}
	ids, err := reqs.Unverified(*reqsDir, ".")
	if err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitRuntime
	}
	rep := unverifiedReport{
		Status: "pending",
		Count:  len(ids),
		List:   ids,
	}
	if len(ids) == 0 {
		rep.Status = "done"
		rep.List = []string{}
	}
	enc, err := json.Marshal(rep)
	if err != nil {
		// json.Marshal of a fixed-shape struct cannot realistically fail,
		// but the surface is non-trivial enough to keep an explicit
		// fallback path rather than ignoring the error.
		fmt.Fprintf(stderr, "ralph: marshal report: %s\n", err)
		return exitRuntime
	}
	fmt.Fprintln(stdout, string(enc))
	return exitSuccess
}

// runLoop parses the loop subcommand's flags, builds a [loop.Config]
// with [loop.Option]s, and hands off to [loop.Run]. It also services
// `--version`/`-v` and `--help`/`-h`, which the flag package allows in
// any position.
func runLoop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { writeUsage(stderr) }

	var (
		reqs        = fs.String("reqs", defaultReqs, "path to requirements directory, relative to the project root")
		appRoot     = fs.String("app-root", defaultAppRoot, "path to the application source subdirectory, relative to the project root")
		engine      = fs.String("engine", defaultEngine, "engine command (drop-in claude replacement) resolved via $PATH")
		model       = fs.String("model", defaultModel, "model alias forwarded to the engine; must have a pricing entry in internal/pricing")
		effort      = fs.String("effort", defaultEffort, "effort level forwarded to the engine (engine-specific; e.g. low|medium|high|xhigh|max for claude)")
		duration    time.Duration
		configDir   = fs.String("config-dir", defaultConfigDir, "value exported as CLAUDE_CONFIG_DIR; empty inherits claude's default (~/.claude)")
		oneM        = fs.Bool("1m-context", defaultOneMContext, "enable 1M-token context window")
		mcp         = fs.Bool("enable-claudeai-mcp-servers", defaultClaudeAIMCP, "enable Claude.ai-managed MCP servers")
		skipPerm    = fs.Bool("dangerously-skip-permissions", defaultSkipPermissions, "pass --dangerously-skip-permissions to claude")
		tools       = fs.String("tools", defaultTools, "comma-separated tool list; empty means all built-ins")
		verbose     = fs.Bool("verbose", false, "echo low-signal stream events (system init, rate_limit)")
		raw         = fs.Bool("raw", false, "debug passthrough: dump engine stdout verbatim as JSONL, suppress all decoration, run one iteration")
		outputLines = fs.Int("output-lines", defaultOutputLines, "max lines of tool output to replay per result before truncating with `...`")

		showVersion bool
		showHelp    bool
	)
	fs.DurationVar(&duration, "duration", defaultDuration, "wall-clock budget (e.g. 4h, 90m); 0 means unlimited")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&showVersion, "v", false, "print version and exit (shorthand)")
	fs.BoolVar(&showHelp, "help", false, "print the operator manual and exit")
	fs.BoolVar(&showHelp, "h", false, "print the operator manual and exit (shorthand)")

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	// --version / --help are honored regardless of where they appear
	// among other flags. They beat the cwd foot-gun check so that
	// `ralph --reqs=foo --version` prints the version cleanly.
	if showVersion {
		fmt.Fprintf(stdout, "ralph %s\n", version)
		return exitSuccess
	}
	if showHelp {
		writeUsagePaged(stdout)
		return exitSuccess
	}

	if *engine == "" {
		fmt.Fprintln(stderr, "ralph: --engine must not be empty")
		return exitUsage
	}

	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "ralph: at most one positional argument (PROJECT_ROOT)")
		writeUsage(stderr)
		return exitUsage
	}
	projectRoot := "."
	if fs.NArg() == 1 {
		projectRoot = fs.Arg(0)
	}
	if err := os.Chdir(projectRoot); err != nil {
		fmt.Fprintf(stderr, "ralph: chdir %q: %s\n", projectRoot, err)
		return exitUsage
	}
	if err := checkProjectRoot(*appRoot); err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitUsage
	}

	theme := ui.NewTheme(os.Stdout)
	stopResize := theme.WatchResize(os.Stdout)
	defer stopResize()

	cfg := loop.Config{
		ReqsDir: *reqs,
		WorkDir: *appRoot,
		Prompt:  kickoffPrompt,
		Theme:   theme,
	}
	opts := []loop.Option{
		loop.WithEngine(*engine),
		loop.WithModel(*model),
		loop.WithEffort(*effort),
		loop.WithVersion(version),
		loop.WithDuration(duration),
		loop.WithConfigDir(*configDir),
		loop.WithTools(*tools),
		loop.WithOneMContext(*oneM),
		loop.WithClaudeAIMCP(*mcp),
		loop.WithSkipPermissions(*skipPerm),
		loop.WithVerbose(*verbose),
		loop.WithRaw(*raw),
		loop.WithOutputLines(*outputLines),
	}

	if err := loop.Run(context.Background(), cfg, opts...); err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitRuntime
	}
	return exitSuccess
}

// checkProjectRoot verifies that cwd looks like a ralph project root:
// the configured app-root subdirectory must exist and must contain an
// AGENTS.md. A missing AGENTS.md is treated the same as a missing
// directory because the agent depends on it as its standing-instructions
// file; running without it would yield a confused, persona-less agent.
func checkProjectRoot(appRoot string) error {
	marker := filepath.Join(appRoot, "AGENTS.md")
	if _, err := os.Lstat(marker); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("no %s found; run `ralph` from a project root scaffolded by `ralph init` (or pass --app-root)", marker)
		}
		return fmt.Errorf("stat %s: %w", marker, err)
	}
	return nil
}
