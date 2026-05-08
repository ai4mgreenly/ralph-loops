package loop

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRun_RejectsEmptyConfig(t *testing.T) {
	err := Run(Config{})
	if err == nil {
		t.Fatal("expected error from empty Config, got nil")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "ReqsDir is required") {
		t.Errorf("expected ReqsDir mention in error, got %v", err)
	}
}

func TestRun_RejectsBadDuration(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Duration = "not-a-duration"
	err := Run(cfg)
	if err == nil {
		t.Fatal("expected error for bad duration")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestParseBudget(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, true},
		{"30s", 30 * time.Second, true},
		{"90m", 90 * time.Minute, true},
		{"4h", 4 * time.Hour, true},
		{"1h30m", 90 * time.Minute, true},
		{"banana", 0, false},
	}
	for _, tc := range tests {
		got, err := parseBudget(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("parseBudget(%q) errored: %v", tc.in, err)
				continue
			}
			if got != tc.want {
				t.Errorf("parseBudget(%q) = %v, want %v", tc.in, got, tc.want)
			}
		} else if err == nil {
			t.Errorf("parseBudget(%q) should have errored", tc.in)
		}
	}
}

func TestFormatBudget(t *testing.T) {
	if got := formatBudget(0); got != "unlimited" {
		t.Errorf("formatBudget(0) = %q, want \"unlimited\"", got)
	}
	if got := formatBudget(2 * time.Hour); got != "2h0m0s" {
		t.Errorf("formatBudget(2h) = %q", got)
	}
}

func minimalValidConfig() Config {
	return Config{
		ReqsDir:  "reqs",
		WorkDir:  ".",
		Model:    "opus",
		Effort:   "medium",
		Duration: "1h",
		Prompt:   "operator prompt",
		Version:  "test",
	}
}
