// Command ralph drives an iterative build agent (the "ralph loop")
// against a project's requirements directory.
//
// Run `ralph help` for a full operator manual. The minimal invocation
// is `ralph WORKDIR`, which uses default values for every flag.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
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

// promptTemplate is the operator prompt sent to claude at the start of
// every iteration. It is embedded from prompt.md at build time, then
// passed through literal string substitution before being sent as the
// kickoff message. Supported placeholders:
//
//	{{REQS}}     replaced with the value of --reqs (default "reqs")
//	{{WORKDIR}}  replaced with the WORKDIR positional argument
//
// Any other `{{...}}` token is sent through verbatim. To test changes,
// edit prompt.md, run `make build`, and invoke `bin/ralph` against a
// scratch directory; unit tests for the substitution logic live in
// cmd/ralph.
//
//go:embed prompt.md
var promptTemplate string

// Default values for every flag the loop subcommand accepts. Centralised
// here so the help text and the FlagSet stay in sync.
const (
	defaultReqs            = "reqs"
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
		if len(args) != 2 {
			fmt.Fprintln(stderr, "ralph init: requires exactly one PATH argument")
			return exitUsage
		}
		if err := scaffoldReqs(args[1]); err != nil {
			fmt.Fprintf(stderr, "ralph: %s\n", err)
			return exitRuntime
		}
		return exitSuccess
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
// recorded in the current workdir's verification ledger. It is the
// single-tool-call replacement for the prompt's grep + jsonl + diff
// procedure: faster, deterministic, cheap on the agent's context
// budget, and — because the report is always JSON — never produces
// ambiguous empty output that an agent would feel compelled to retry.
//
// The workdir is the process's current working directory. Ralph spawns
// the agent with cwd set to WORKDIR, so the agent (and any human
// running the command from a project root) gets the right answer with
// no positional argument to manage.
func runUnverified(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph unverified", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reqsDir := fs.String("reqs", defaultReqs, "path to requirements directory")
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
		reqs        = fs.String("reqs", defaultReqs, "path to requirements directory")
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
	// among other flags. They beat WORKDIR validation so that
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

	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "ralph: WORKDIR positional argument is required")
		writeUsage(stderr)
		return exitUsage
	}
	workdir := fs.Arg(0)

	prompt := strings.NewReplacer(
		"{{REQS}}", *reqs,
		"{{WORKDIR}}", workdir,
	).Replace(promptTemplate)

	theme := ui.NewTheme(os.Stdout)
	stopResize := theme.WatchResize(os.Stdout)
	defer stopResize()

	cfg := loop.Config{
		ReqsDir: *reqs,
		WorkDir: workdir,
		Prompt:  prompt,
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
