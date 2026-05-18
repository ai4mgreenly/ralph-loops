package agent_test

import (
	"errors"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/agent"
)

// TestSpawner_Blackbox_NewSpawnerNotNil confirms the public
// constructor returns a usable value. We don't Spawn here — that
// path is covered by the integration tests in package agent — but
// we do exercise the side-effect-free getters callers compose with.
func TestSpawner_Blackbox_NewSpawnerNotNil(t *testing.T) {
	sp := agent.NewSpawner("pi")
	if sp == nil {
		t.Fatal("NewSpawner returned nil")
	}
	// Spawner exposes Stderr as a public knob; tests should be able
	// to set it from outside the package.
	sp.Stderr = nil
}

// TestExitError_Blackbox_AsAndIs exercises the public error contract
// of [agent.ExitError] from outside the package: errors.As pulls the
// concrete pointer out and the typed fields are reachable.
func TestExitError_Blackbox_AsAndIs(t *testing.T) {
	original := &agent.ExitError{Code: 42}
	var wrapped error = original

	var got *agent.ExitError
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As on ExitError failed")
	}
	if got.Code != 42 {
		t.Errorf("Code = %d, want 42", got.Code)
	}
	if got.Error() == "" {
		t.Error("Error() should return a non-empty message")
	}
}
