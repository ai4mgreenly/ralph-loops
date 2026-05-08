package loop

import (
	"strings"
	"testing"
)

func TestCorrectionMessage_MentionsSchema(t *testing.T) {
	got := correctionMessage(errBadStructuredOutput)
	for _, sub := range []string{"DONE", "CONTINUE", "structured"} {
		if !strings.Contains(got, sub) {
			t.Errorf("correction message missing %q: %q", sub, got)
		}
	}
}
