// Package loop drives the ralph iteration loop: it asks a [Spawner] for
// a fresh one-shot pi session per iteration, feeds it the kickoff
// prompt (baked into pi's argv at spawn), parses pi's native JSONL
// event flow, and repeats until the agent's terminal `agent_end`
// carries a DONE sentinel, the wall-clock budget is exhausted, or the
// operator presses Ctrl-C.
//
// The package is split across these files:
//
//   - loop.go      Config, Run, the outer loop and signal plumbing.
//   - iteration.go One pi invocation: spawn, single event pump (no
//     correction retry — pi is one-shot), Q3 status decode.
//   - stats.go     Run-wide counters; the agent_end-driven token/cost
//     tally and panel renderer.
//   - raw.go       The de-decorated `--raw` passthrough.
//
// Subprocess mechanics (os/exec, process groups) live in the sibling
// [internal/agent] package; per-event rendering lives in
// [internal/render]. loop owns lifecycle and outcome interpretation.
package loop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
	"github.com/ai4mgreenly/ralph-loops/internal/render"
	"github.com/ai4mgreenly/ralph-loops/internal/stream"
	"github.com/ai4mgreenly/ralph-loops/internal/ui"
)

// piBinary is the engine command ralph spawns each iteration. Under the
// pi migration ralph is pi-exclusive: there is no --engine knob.
const piBinary = "pi"

// Default values for the loop's options. The CLI surfaces the same set
// (with the same defaults) at the flag layer.
const (
	defaultVersion     = "dev"
	defaultOutputLines = 0 // 0 means "let the emitter pick"
)

// Config carries the small set of values [Run] insists on. Everything
// else is optional and threaded through [Option] arguments. A zero
// value is rejected with [ErrInvalidConfig].
type Config struct {
	// ReqsDir is the path to the project's requirements directory.
	// The agent reads from but never writes to this tree.
	ReqsDir string

	// WorkDir is the path to the application source tree. The agent
	// reads and writes inside this tree. [Run] creates the directory
	// (with any missing parents) before the first iteration.
	WorkDir string

	// Prompt is the kickoff message fed to pi at the start of each
	// iteration. It rides on pi's trailing positional argv.
	Prompt string

	// SystemPromptFile is the ABSOLUTE path to the build-agent
	// AGENTS.md (under app-root). It is forwarded to pi as
	// --append-system-prompt; an empty value omits the flag (pi then
	// uses only its base system prompt). The caller computes the
	// absolute path; the loop does not do AGENTS.md walk-up.
	SystemPromptFile string

	// Theme owns the colour and width state shared by every rendering
	// helper in this run. Construct via [ui.NewTheme] (or
	// [ui.NewThemeWith] in tests).
	Theme *ui.Theme
}

// Option configures one knob on a [Run] invocation. Pass options to
// [Run] rather than mutating fields after the fact.
type Option func(*options)

// options is the private bag of resolved option values: the
// model/tooling knobs ralph forwards to pi, behaviour switches, and
// test seams (clock, results path, spawner).
type options struct {
	model       string
	provider    string
	thinking    string
	version     string
	tools       string
	duration    time.Duration
	verbose     bool
	raw         bool
	outputLines int
	now         func() time.Time
	resultsHome string
	// spawner, when non-nil, overrides the production [Spawner] inside
	// [Run]. Set via [WithSpawner]; production callers leave it nil so
	// [defaultSpawner] supplies the real pi-backed implementation.
	spawner Spawner
}

// defaultOptions produces the option struct populated with documented
// defaults; option functions then layer on top.
func defaultOptions() options {
	return options{
		version:     defaultVersion,
		outputLines: defaultOutputLines,
	}
}

// WithModel forwards a model identifier to pi as --model. ralph does
// not parse it: pi's provider/id and model:thinking forms pass through
// opaque. Empty (the default) omits the flag so pi uses its own
// configured default.
func WithModel(m string) Option { return func(o *options) { o.model = m } }

// WithProvider forwards a provider id to pi as --provider. Empty (the
// default) omits the flag so pi falls back to its own settings. Plumbed
// for a later slice to surface as a cmd flag.
func WithProvider(p string) Option { return func(o *options) { o.provider = p } }

// WithThinking forwards a thinking level to pi as --thinking. pi
// validates the level itself. Empty (the default) omits the flag.
// Plumbed for a later slice to surface as a cmd flag.
func WithThinking(t string) Option { return func(o *options) { o.thinking = t } }

// WithVersion sets the version string included in the run banner.
// Default: "dev".
func WithVersion(v string) Option { return func(o *options) { o.version = v } }

// WithTools forwards a comma-separated tool list to pi as --tools.
// Empty (the default) lets the agent layer apply pi's built-in
// allowlist.
func WithTools(t string) Option { return func(o *options) { o.tools = t } }

// WithDuration sets the wall-clock cap for the run. Zero (the default)
// means unlimited; negative values are rejected by [Run].
func WithDuration(d time.Duration) Option { return func(o *options) { o.duration = d } }

// WithVerbose toggles the rendering of low-signal stream events (the
// pi session banner and known-but-unused carriers) into the operator
// log.
func WithVerbose(v bool) Option { return func(o *options) { o.verbose = v } }

// WithRaw enables debug passthrough: the loop suppresses every
// rendering decorator (banner, iteration headers, spinner, formatted
// events, stats panel, results.jsonl) and instead taps pi's stdout,
// copying every byte pi emits to the run writer verbatim. The trace is
// prefixed with a `{"type":"_ralph_kickoff","prompt":"..."}` envelope
// so it records its own input. Exactly one iteration runs; nothing is
// parsed. Intended for diagnosing pi wire traces.
func WithRaw(v bool) Option { return func(o *options) { o.raw = v } }

// WithOutputLines caps the number of tool-output lines replayed per
// result before truncation. A value <= 0 leaves the emitter's own
// default in place.
func WithOutputLines(n int) Option { return func(o *options) { o.outputLines = n } }

// WithNow installs a deterministic clock for tests. Production code
// should leave it unset, in which case [time.Now] is used.
func WithNow(now func() time.Time) Option { return func(o *options) { o.now = now } }

// WithResultsHome overrides the default results-log directory
// (~/.ralph-loops). An empty string disables the log entirely.
func WithResultsHome(p string) Option { return func(o *options) { o.resultsHome = p } }

// WithSpawner installs a custom [Spawner] in place of the production
// one backed by the pi CLI. Intended for tests: it lets a [Run]
// invocation be driven by a fake spawner so the entire loop —
// validation, signal handling, results-JSONL log — can be exercised
// without forking a subprocess. Production code should leave this
// unset.
func WithSpawner(s Spawner) Option { return func(o *options) { o.spawner = s } }

// ErrInvalidConfig is returned by [Run] when [Config] or an [Option]
// fails validation. Callers can use errors.Is to detect the case.
var ErrInvalidConfig = errors.New("invalid config")

// ErrInterrupted is returned when the run was halted by a signal
// (typically Ctrl-C) before reaching a natural DONE.
var ErrInterrupted = errors.New("interrupted")

// ErrTimedOut is returned when the wall-clock budget set by
// [WithDuration] expires before the agent reports DONE.
var ErrTimedOut = errors.New("duration budget exhausted")

// exitReason classifies how a run terminated. Its [exitReason.String]
// renders as the empty string for the zero value so a panel printed
// mid-run can omit the `exit:` line entirely.
type exitReason int

const (
	exitNone exitReason = iota
	exitDone
	exitTimedOut
	exitInterrupted
	exitErrored
)

// String returns the human-readable label for r used in the panel and
// the results.jsonl record. The lowercase strings are the JSON wire
// shape.
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

// Run validates cfg, applies opts, sets up signal handling, and drives
// the iteration loop until DONE, the budget expires, or a signal
// arrives. The final stats panel is always written to stdout; the
// returned error indicates how the run terminated.
//
// The supplied ctx is the parent of every derived context: cancelling
// it short-circuits the run cleanly. Run still installs its own
// SIGINT/SIGTERM handler on top of ctx, because most callers
// (including [cmd/ralph]) pass [context.Background] and rely on Run
// for signal handling.
//
// [Config.WorkDir] is created (with any missing parents) before the
// first iteration spawns, so operators can point ralph at a not-yet-
// existing scratch directory without a preparatory `mkdir`.
//
// Run is single-shot: spawn a fresh Run per process. Reusing one
// Config across two concurrent calls is not supported.
func Run(ctx context.Context, cfg Config, opts ...Option) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}
	if err := validate(cfg, o); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("create workdir %q: %w", cfg.WorkDir, err)
	}
	sp := o.spawner
	if sp == nil {
		// Resolve pi against $PATH up front so a missing or unreachable
		// binary crashes loudly before we print the banner or open the
		// results log, rather than failing opaquely on the first
		// iteration.
		if _, err := exec.LookPath(piBinary); err != nil {
			return fmt.Errorf("engine %q not found in PATH: %w", piBinary, err)
		}
		sp = defaultSpawner(o.raw, os.Stdout)
	}
	return runWith(ctx, cfg, o, os.Stdout, sp)
}

// defaultSpawner returns the production [Spawner] backed by the pi CLI.
// *agent.Spawner satisfies the consumer-side [Spawner] interface
// directly because [agent.Spawner.Spawn] returns the [agent.Session]
// interface, not a concrete type.
//
// When raw is true, the spawner taps pi's stdout into tap so every byte
// pi emits is mirrored verbatim — the substrate for [WithRaw].
func defaultSpawner(raw bool, tap io.Writer) Spawner {
	sp := agent.NewSpawner(piBinary)
	if raw {
		sp.Stdout = tap
	}
	return sp
}

// runWith is the testable kernel of [Run]. It assumes cfg is already
// validated and writes the banner and stats panel to w. The spawner
// seam lets tests drive a full run with no subprocess.
func runWith(ctx context.Context, cfg Config, o options, w io.Writer, sp Spawner) error {
	// First SIGINT/SIGTERM cancels the context, giving the run a chance
	// to wind down gracefully.
	sigCtx, stopSig := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSig()

	runCtx, cancel := withBudget(sigCtx, o.duration)
	defer cancel()

	// Once the first SIGINT cancels sigCtx, give the run a fixed grace
	// window to wind down on its own. If it is still running when the
	// deadline fires, force a hard exit — the operator should not have
	// to babysit a stuck shutdown.
	stopForce := installShutdownDeadline(sigCtx, forceQuitDeadline, w, os.Exit)
	defer stopForce()

	if o.raw {
		return runRaw(runCtx, cfg, o, w, sp)
	}

	now := o.now
	if now == nil {
		now = time.Now
	}
	resultsHome := o.resultsHome
	if resultsHome == "" {
		resultsHome = defaultResultsHomePath()
	}

	// The banner reuses ui.Header's existing shape; under pi-exclusive
	// operation the engine column is always "pi" and there is no
	// effort knob, so that slot is left blank.
	ui.Header(w, o.version, piBinary, o.model, "", formatBudget(o.duration))
	fmt.Fprintf(w, "reqs=%s\n", cfg.ReqsDir)

	s := newStats(o.model, now, resultsHome)
	e := render.NewEmitter(
		w, s, cfg.Theme,
		render.WithVerbose(o.verbose),
		render.WithOutputLines(o.outputLines),
		render.WithSpinnerLabel(spinnerLabel(o.model)),
	)

	exit, runErr := drive(runCtx, cfg, o, sp, e, s)
	sum := s.snapshot(realPath(cfg.ReqsDir), exit)
	sum.writeText(w, cfg.Theme.Width())
	appendResultsJSONL(resultsHome, sum)

	return runErr
}

// spinnerLabel picks the "waiting for X" suffix: the model name when an
// operator pinned one, otherwise the engine name so a bare invocation
// still reads naturally.
func spinnerLabel(model string) string {
	if model != "" {
		return model
	}
	return piBinary
}

// drive runs successive iterations until ctx is cancelled or pi's
// terminal agent_end carries a DONE sentinel. It returns the exit
// reason for the panel and either nil, [ErrInterrupted], [ErrTimedOut],
// or a wrapped runtime error.
func drive(ctx context.Context, cfg Config, o options, sp Spawner, e *render.Emitter, s *stats) (exitReason, error) {
	for {
		// Check for cancellation between iterations as well as during
		// them; an iteration that finishes the same instant as a
		// signal arrives shouldn't queue another.
		if err := ctx.Err(); err != nil {
			return ctxExit(err)
		}

		s.incrementIteration()
		e.IterationBanner(s.iterationCount())
		status, err := runIteration(ctx, cfg, o, sp, e, s)
		if err != nil {
			// ctx-cancelled takes precedence over an iteration error
			// (Q3): a run aborted by timeout/interrupt is reported as
			// such, not as a generic failure. The missing-agent_end
			// case (errStreamEnded) reaches here as a plain iteration
			// error only when ctx is NOT cancelled.
			if cErr := ctx.Err(); cErr != nil {
				return ctxExit(cErr)
			}
			return exitErrored, err
		}
		// DONE stops the loop; CONTINUE (explicit, or the safe default
		// StatusFromAgentEnd applies when agent_end carries no parseable
		// sentinel) falls through to the next iteration.
		if status == stream.StatusDone {
			return exitDone, nil
		}
	}
}

// ctxExit translates a context error into a (panel-reason, returned-
// error) pair. Callers must only invoke it after observing ctx.Err()
// != nil, so the only sentinels that can appear here are
// [context.Canceled] and [context.DeadlineExceeded]. A future-proof
// default branch returns a wrapped "unexpected" error rather than
// panicking, so a stray context implementation cannot crash the run.
func ctxExit(err error) (exitReason, error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return exitTimedOut, ErrTimedOut
	case errors.Is(err, context.Canceled):
		return exitInterrupted, ErrInterrupted
	default:
		return exitErrored, fmt.Errorf("unexpected ctx error: %w", err)
	}
}

// withBudget wraps ctx with a deadline if budget is positive,
// otherwise returns a child that inherits no deadline.
func withBudget(parent context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, budget)
}

// forceQuitDeadline is how long [installShutdownDeadline] waits after
// the first interrupt before force-quitting. Long enough that a normal
// graceful shutdown completes inside it; short enough that a stuck run
// doesn't leave the operator hammering Ctrl-C.
const forceQuitDeadline = 10 * time.Second

// installShutdownDeadline arms a force-quit timer that starts when
// sigCtx is canceled (i.e. after the first SIGINT). If the run is
// still alive `deadline` later, we log to w (or stderr if w is nil)
// and call quit(130) — the conventional "terminated by SIGINT" status.
// The returned function disarms the timer and should be deferred; once
// it has run the goroutine exits without firing.
//
// quit is injected so tests can pass a tiny deadline and assert the
// call rather than terminating the test process. Production code
// passes [os.Exit].
func installShutdownDeadline(sigCtx context.Context, deadline time.Duration, w io.Writer, quit func(int)) func() {
	done := make(chan struct{})
	go func() {
		// Wait for the graceful-cancel context to fire before arming the
		// timer; otherwise the deadline would start counting from
		// program launch and a long, healthy run could trip it.
		select {
		case <-sigCtx.Done():
		case <-done:
			return
		}
		t := time.NewTimer(deadline)
		defer t.Stop()
		select {
		case <-t.C:
			out := w
			if out == nil {
				out = os.Stderr
			}
			fmt.Fprintf(out, "ralph: graceful shutdown exceeded %s; force-quitting\n", deadline)
			quit(130)
		case <-done:
		}
	}()

	return func() { close(done) }
}

// realPath returns p resolved to an absolute, symlink-followed path
// suitable for the closing report. If either resolution step fails the
// best partial result is returned, falling back to p unchanged — the
// report should never be derailed by a path that can't be canonicalised.
func realPath(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// formatBudget renders the wall-clock cap for the run banner, with a
// special case for the "no cap" zero value.
func formatBudget(d time.Duration) string {
	if d == 0 {
		return "unlimited"
	}
	return d.String()
}

// validate returns nil if cfg/o have every required field populated,
// or a joined error listing each missing/invalid field otherwise.
// There is no longer a pricing-table gate on --model: pi reports its
// own authoritative per-provider cost, so an unknown model name can no
// longer silently produce a $0.0000 report.
func validate(cfg Config, o options) error {
	required := []struct {
		name  string
		value string
	}{
		{"ReqsDir", cfg.ReqsDir},
		{"WorkDir", cfg.WorkDir},
		{"Prompt", cfg.Prompt},
	}
	var errs []error
	for _, f := range required {
		if f.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", f.name))
		}
	}
	if cfg.Theme == nil {
		errs = append(errs, errors.New("Theme is required"))
	}
	if o.duration < 0 {
		errs = append(errs, fmt.Errorf("duration must be non-negative (got %v)", o.duration))
	}
	return errors.Join(errs...)
}
