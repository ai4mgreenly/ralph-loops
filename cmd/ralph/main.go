// Command ralph drives an iterative build agent (the "ralph loop")
// against a project's requirements directory.
//
// Run `ralph help` for a full operator manual. The minimal invocation
// is `ralph WORKDIR`, which uses default values for every flag.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/cli"
	"github.com/ai4mgreenly/ralph-loops/internal/idgen"
	"github.com/ai4mgreenly/ralph-loops/internal/loop"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// version is the build identifier reported by `ralph version` and
// stamped into the run banner. It is overridden at link time via
// `-ldflags "-X main.version=..."` (see the Makefile); the default
// value here is what unstamped builds (e.g. `go run`) report.
var version = "dev"

//go:embed prompt.md
var promptTemplate string

// Default values for every flag the loop subcommand accepts. Centralised
// here so the help text and the FlagSet stay in sync.
const (
	defaultReqs            = "reqs"
	defaultModel           = "opus"
	defaultEffort          = "medium"
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

// allowedModels and allowedEfforts are the permitted values for their
// respective enumFlag-typed CLI flags.
var (
	allowedModels  = []string{"haiku", "sonnet", "opus"}
	allowedEfforts = []string{"low", "medium", "high", "xhigh", "max"}
)

// usageDefaults snapshots the flag defaults into the shape the manual
// printer wants.
func usageDefaults() cli.UsageDefaults {
	return cli.UsageDefaults{
		Version:         version,
		Reqs:            defaultReqs,
		Model:           defaultModel,
		Effort:          defaultEffort,
		OneMContext:     defaultOneMContext,
		SkipPermissions: defaultSkipPermissions,
		ClaudeAIMCP:     defaultClaudeAIMCP,
		OutputLines:     defaultOutputLines,
	}
}

// enumFlag is a flag.Value implementation that constrains a string flag
// to a fixed set of allowed values. The default value is supplied by
// the caller and considered valid even if it is not in allowed (so we
// can construct an enumFlag without forcing every default into the
// allowed set).
type enumFlag struct {
	value   string
	allowed []string
	name    string
}

// newEnumFlag constructs an enumFlag with the given default and
// allowed-set. name is used in error messages.
func newEnumFlag(name, def string, allowed []string) *enumFlag {
	return &enumFlag{value: def, allowed: allowed, name: name}
}

// String reports the current value. It is also used by the flag
// package to render defaults in usage strings.
func (e *enumFlag) String() string {
	return e.value
}

// Set validates v against the allowed set and stores it on success.
func (e *enumFlag) Set(v string) error {
	for _, ok := range e.allowed {
		if v == ok {
			e.value = v
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q: must be one of %s", e.name, v, strings.Join(e.allowed, "|"))
}

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
		cli.WriteUsage(stderr, usageDefaults())
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
		if err := cli.Init(args[1]); err != nil {
			fmt.Fprintf(stderr, "ralph: %s\n", err)
			return exitRuntime
		}
		return exitSuccess
	case "newid":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "ralph newid: takes no arguments")
			return exitUsage
		}
		fmt.Fprintln(stdout, idgen.New())
		return exitSuccess
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
	case "version":
		fmt.Fprintf(stdout, "ralph %s\n", version)
		return exitSuccess
	case "help":
		cli.WriteUsagePaged(stdout, usageDefaults())
		return exitSuccess
	default:
		return runLoop(args, stdout, stderr)
	}
}

// runLoop parses the loop subcommand's flags, materialises a
// [loop.Config], and hands off to [loop.Run]. It also services
// `--version`/`-v` and `--help`/`-h`, which the flag package allows in
// any position — fixing the longstanding bug where
// `ralph --reqs=foo --version` fell through to the loop driver.
func runLoop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ralph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { cli.WriteUsage(stderr, usageDefaults()) }

	var (
		reqs        = fs.String("reqs", defaultReqs, "path to requirements directory")
		model       = newEnumFlag("--model", defaultModel, allowedModels)
		effort      = newEnumFlag("--effort", defaultEffort, allowedEfforts)
		duration    time.Duration
		configDir   = fs.String("config-dir", defaultConfigDir, "value exported as CLAUDE_CONFIG_DIR; empty inherits claude's default (~/.claude)")
		oneM        = fs.Bool("1m-context", defaultOneMContext, "enable 1M-token context window")
		mcp         = fs.Bool("enable-claudeai-mcp-servers", defaultClaudeAIMCP, "enable Claude.ai-managed MCP servers")
		skipPerm    = fs.Bool("dangerously-skip-permissions", defaultSkipPermissions, "pass --dangerously-skip-permissions to claude")
		tools       = fs.String("tools", defaultTools, "comma-separated tool list; empty means all built-ins")
		verbose     = fs.Bool("verbose", false, "echo low-signal stream events (system init, rate_limit)")
		outputLines = fs.Int("output-lines", defaultOutputLines, "max lines of tool output to replay per result before truncating with `...`")

		showVersion bool
		showHelp    bool
	)
	fs.Var(model, "model", "haiku|sonnet|opus")
	fs.Var(effort, "effort", "low|medium|high|xhigh|max")
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
		cli.WriteUsagePaged(stdout, usageDefaults())
		return exitSuccess
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "ralph: WORKDIR positional argument is required")
		cli.WriteUsage(stderr, usageDefaults())
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
		ReqsDir:         *reqs,
		WorkDir:         workdir,
		Model:           model.String(),
		Effort:          effort.String(),
		Duration:        duration,
		ConfigDir:       *configDir,
		OneMContext:     *oneM,
		ClaudeAIMCP:     *mcp,
		SkipPermissions: *skipPerm,
		Tools:           *tools,
		Prompt:          prompt,
		Version:         version,
		Verbose:         *verbose,
		OutputLines:     *outputLines,
		Theme:           theme,
	}

	if err := loop.Run(cfg); err != nil {
		fmt.Fprintf(stderr, "ralph: %s\n", err)
		return exitRuntime
	}
	return exitSuccess
}
