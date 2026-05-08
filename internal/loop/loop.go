// Package loop drives the ralph iteration loop: it asks a
// [Spawner] for a fresh agent session per iteration, feeds it
// the operator prompt, parses the stream-json event flow, and repeats
// until the agent reports DONE, the wall-clock budget is exhausted,
// or the operator presses Ctrl-C.
//
// The package is split across three files:
//
//   - loop.go      Config, Run, the outer loop and signal plumbing.
//   - iteration.go One agent invocation: kickoff, event pump, retry.
//   - stats.go     Run-wide counters, token/cost tallies, panel renderer.
//
// Subprocess mechanics (os/exec, the user-message envelope, process
// groups) live in the sibling [internal/agent] package; per-event
// rendering lives in [internal/render]. loop owns lifecycle.
package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/pricing"
	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// allowedEfforts is the set of accepted values for [Config.Effort].
// The canonical list lives in the [cmd/ralph] CLI; this duplicate
// gives the package a defence-in-depth check at its boundary.
//
// TODO: collapse the two copies once an effort registry exists.
var allowedEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// Config is the full set of inputs to a ralph run.
//
// String fields marked "required" must be non-empty; [Run] returns
// an error wrapping [ErrInvalidConfig] otherwise. Optional string
// fields have a documented zero-value meaning. Boolean fields default
// to whatever the caller supplies; the [cmd/ralph] CLI picks defaults
// that match the original ralph-scoops Ruby driver.
type Config struct {
	// ReqsDir is the path to the project's requirements directory.
	// The agent reads from but never writes to this tree. Required.
	ReqsDir string

	// WorkDir is the path to the application source tree. The agent
	// reads and writes inside this tree. Required.
	WorkDir string

	// Model is one of "haiku", "sonnet", or "opus". Required.
	Model string

	// Effort is one of "low", "medium", "high", "xhigh", or "max".
	// Required.
	Effort string

	// Duration is the wall-clock budget for the run. Zero means the
	// run is unbounded; negative values are rejected by [Run].
	Duration time.Duration

	// ConfigDir is exported to the child process as CLAUDE_CONFIG_DIR
	// when non-empty. An empty string leaves the env var unset, which
	// makes claude fall back to its own default (~/.claude).
	ConfigDir string

	// OneMContext enables the 1M-token context window when true.
	OneMContext bool

	// ClaudeAIMCP enables Claude.ai-managed MCP servers when true.
	ClaudeAIMCP bool

	// SkipPermissions passes --dangerously-skip-permissions through
	// to the claude CLI when true.
	SkipPermissions bool

	// Tools is forwarded verbatim to claude --tools when non-empty.
	Tools string

	// Prompt is the operator prompt to feed to the agent at the
	// start of each iteration. Path placeholders should already be
	// substituted.
	Prompt string

	// Version is the ralph version string included in the run banner.
	Version string

	// Verbose controls whether low-signal stream events — currently
	// `system` (init / tool list / permission mode) and `rate_limit` —
	// are echoed to the operator. Off by default; enabled via
	// `--verbose` for debugging or detailed run inspection.
	Verbose bool

	// OutputLines is the maximum number of lines of tool output
	// (Bash stdout/stderr, Read file contents, Edit/Write hunks)
	// replayed in the activity log per result. Zero falls back to the
	// emitter's built-in default.
	OutputLines int

	// Theme owns the colour and width state used by every rendering
	// helper in this run. Required: callers must construct one via
	// [ui.NewTheme] (or [ui.NewThemeWith] in tests) so the loop and
	// the emitter share a single source of truth.
	Theme *ui.Theme

	// ResultsHome is the directory where the JSONL results log is
	// written. An empty string falls back to ~/.ralph-loops (or no
	// log at all when the user's home directory cannot be determined).
	// Tests typically point this at a t.TempDir().
	ResultsHome string

	// Now is the wall-clock source threaded into stats. Zero falls
	// back to [time.Now]; tests pin this to a fixed time so timestamp
	// assertions can be exact.
	Now func() time.Time
}

// ErrInvalidConfig is returned by [Run] when the supplied [Config]
// fails validation. Callers can use errors.Is to detect the case.
var ErrInvalidConfig = errors.New("invalid config")

// ErrInterrupted is returned when the run was halted by a signal
// (typically Ctrl-C) before reaching a natural DONE.
var ErrInterrupted = errors.New("interrupted")

// ErrTimedOut is returned when the wall-clock budget set by
// [Config.Duration] expires before the agent reports DONE.
var ErrTimedOut = errors.New("duration budget exhausted")

// exitReason classifies how a run terminated. It prints (and JSON-
// marshals) as a short noun matching the legacy string constants
// ("done", "timeout", "interrupted", "error"); the zero value
// [exitNone] renders as the empty string so a panel printed mid-run
// can omit the `exit:` line entirely.
type exitReason int

const (
	exitNone exitReason = iota
	exitDone
	exitTimedOut
	exitInterrupted
	exitErrored
)

// String returns the human-readable label for r used in the panel and
// the results.jsonl record. Unknown values produce "unknown" rather
// than panicking so a stray default-zero from a future refactor stays
// debuggable.
func (r exitReason) String() string {
	switch r {
	case exitNone:
		return ""
	case exitDone:
		return "done"
	case exitTimedOut:
		return "timeout"
	case exitInterrupted:
		return "interrupted"
	case exitErrored:
		return "error"
	default:
		return "unknown"
	}
}

// Run validates cfg, sets up signal handling, and drives the
// iteration loop until DONE, the budget expires, or a signal arrives.
// The final stats panel is always written to stdout; the returned
// error indicates how the run terminated.
//
// As a convenience, [Config.WorkDir] is created (with any missing
// parents) before the first iteration spawns. This lets operators
// point ralph at a not-yet-existing scratch directory without a
// preparatory `mkdir`.
func Run(cfg Config) error {
	if err := validate(cfg); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("create workdir %q: %w", cfg.WorkDir, err)
	}
	return runWith(cfg, cfg.Duration, os.Stdout, newClaudeSpawner())
}

// runWith is the testable kernel of [Run]. It assumes inputs are
// already validated and writes the banner and stats panel to w. The
// spawner seam lets tests drive a full run with no subprocess.
func runWith(cfg Config, budget time.Duration, w io.Writer, sp Spawner) error {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	resultsHome := cfg.ResultsHome
	if resultsHome == "" {
		resultsHome = defaultResultsHomePath()
	}

	ui.Header(w, cfg.Version, cfg.Model, cfg.Effort, formatBudget(budget))
	fmt.Fprintf(w, "reqs=%s\nworkdir=%s\n\n", cfg.ReqsDir, cfg.WorkDir)

	// First SIGINT/SIGTERM cancels the context, giving the run a chance
	// to wind down gracefully. signal.NotifyContext (Go 1.16+) replaces
	// the hand-rolled goroutine that used to live here.
	sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSig()

	ctx, cancel := withBudget(sigCtx, budget)
	defer cancel()

	// A second SIGINT after the context is already canceled is the
	// operator telling us "I'm done waiting." Translate it into a
	// hard exit. We register a separate channel because NotifyContext
	// stops listening once it has fired.
	stopForce := installForceQuit(sigCtx, w, os.Exit)
	defer stopForce()

	s := newStats(cfg.Model, now, resultsHome)
	e := render.NewEmitter(
		w, s, cfg.Theme,
		render.WithVerbose(cfg.Verbose),
		render.WithOutputLines(cfg.OutputLines),
	)

	exitReason, runErr := drive(ctx, cfg, sp, e, s)
	sum := s.snapshot(cfg.ReqsDir, exitReason)
	sum.writeText(w, cfg.Theme.Width())
	appendResultsJSONL(resultsHome, sum)

	return runErr
}

// drive runs successive iterations until ctx is cancelled or claude
// returns DONE. It returns the exit reason for the panel and either
// nil, [ErrInterrupted], [ErrTimedOut], or a wrapped runtime error.
func drive(ctx context.Context, cfg Config, sp Spawner, e *render.Emitter, s *stats) (exitReason, error) {
	for {
		// Check for cancellation between iterations as well as during
		// them; an iteration that finishes the same instant as a
		// signal arrives shouldn't queue another.
		if err := ctx.Err(); err != nil {
			return ctxExit(err)
		}

		s.incrementIteration()
		e.IterationBanner(s.iterations)
		status, err := runIteration(ctx, cfg, sp, e, s)
		if err != nil {
			if cErr := ctx.Err(); cErr != nil {
				return ctxExit(cErr)
			}
			return exitErrored, err
		}
		if status == stream.StatusDone {
			return exitDone, nil
		}
	}
}

// ctxExit translates a context error into a (panel-reason, returned-
// error) pair. Callers must only invoke it after observing ctx.Err()
// != nil, so the only sentinels that can appear here are
// [context.Canceled] and [context.DeadlineExceeded].
func ctxExit(err error) (exitReason, error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return exitTimedOut, ErrTimedOut
	case errors.Is(err, context.Canceled):
		return exitInterrupted, ErrInterrupted
	}
	panic("unreachable: ctx.Err() returned non-Canceled/DeadlineExceeded: " + err.Error())
}

// withBudget wraps ctx with a deadline if budget is positive,
// otherwise returns a child that inherits no deadline.
func withBudget(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, budget)
}

// installForceQuit watches for a second SIGINT arriving after sigCtx
// has already been canceled by the first one. When that happens the
// operator has signalled they are unwilling to wait for graceful
// shutdown, so we log to w (or stderr if w is nil) and call quit(130)
// — the conventional "terminated by SIGINT" status. The returned
// function uninstalls the handler and should be deferred.
//
// quit is injected so tests can drive a real second-SIGINT and assert
// the call rather than terminating the test process. Production code
// passes [os.Exit].
func installForceQuit(sigCtx context.Context, w io.Writer, quit func(int)) func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	done := make(chan struct{})
	go func() {
		// Wait for the graceful-cancel context to fire before we start
		// listening for a "second" signal — otherwise we'd race with
		// NotifyContext and could turn a single Ctrl-C into a hard exit.
		select {
		case <-sigCtx.Done():
		case <-done:
			return
		}
		select {
		case <-sigCh:
			out := w
			if out == nil {
				out = os.Stderr
			}
			fmt.Fprintln(out, "ralph: received second interrupt; force-quitting")
			quit(130)
		case <-done:
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}

// formatBudget renders the wall-clock cap for the run banner, with a
// special case for the "no cap" zero value.
func formatBudget(d time.Duration) string {
	if d == 0 {
		return "unlimited"
	}
	return d.String()
}

// validate returns nil if cfg has every required field populated, or
// a joined error listing each missing field otherwise.
func validate(cfg Config) error {
	required := []struct {
		name  string
		value string
	}{
		{"ReqsDir", cfg.ReqsDir},
		{"WorkDir", cfg.WorkDir},
		{"Model", cfg.Model},
		{"Effort", cfg.Effort},
		{"Prompt", cfg.Prompt},
		{"Version", cfg.Version},
	}
	var errs []error
	for _, f := range required {
		if f.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", f.name))
		}
	}
	if cfg.Duration < 0 {
		errs = append(errs, fmt.Errorf("Duration must be non-negative (got %v)", cfg.Duration))
	}
	if cfg.Theme == nil {
		errs = append(errs, fmt.Errorf("Theme is required"))
	}
	if cfg.Model != "" && !pricing.HasModel(cfg.Model) {
		errs = append(errs, fmt.Errorf("Model %q is not a known alias", cfg.Model))
	}
	if cfg.Effort != "" && !contains(allowedEfforts, cfg.Effort) {
		errs = append(errs, fmt.Errorf("Effort %q must be one of %v", cfg.Effort, allowedEfforts))
	}
	return errors.Join(errs...)
}

// contains reports whether s is in xs. A small helper kept local to
// the package to avoid pulling in slices for one call site.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
