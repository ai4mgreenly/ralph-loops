package stream_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// FuzzReader_Next feeds arbitrary bytes through [stream.Reader] and
// asserts the contract every consumer relies on:
//
//   - [stream.Reader.Next] never panics, no matter how malformed the
//     input is;
//   - the (event, error) return is well-formed: a nil-error result has
//     a non-nil event, and any non-EOF error has a non-empty message;
//   - draining an [stream.AgentEnd] and applying [stream.StatusFromAgentEnd]
//     never panics and never returns [stream.StatusUnknown] (the parser
//     always commits to DONE or CONTINUE).
//
// The seed corpus pairs real pi captures (so the fuzzer has structured
// material to mutate) with a few representative malformed shapes.
func FuzzReader_Next(f *testing.F) {
	for _, name := range []string{"done.jsonl", "continue.jsonl", "no-sentinel.jsonl", "truncated.jsonl"} {
		if b, err := os.ReadFile(filepath.Join("testdata", name)); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`{"type":"session","version":3,"id":"x","timestamp":"t","cwd":"/"}` + "\n"))
	f.Add([]byte(`{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"RALPH-STATUS: DONE"}]}]}` + "\n"))
	f.Add([]byte("not valid json\n"))
	f.Add([]byte(`{"type":"unrecognised_kind"}` + "\n"))
	f.Add([]byte(`{"type":"agent_end","messages":"oops"}` + "\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		r := stream.NewReader(bytes.NewReader(data))
		for {
			ev, err := r.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				if err.Error() == "" {
					t.Fatalf("error with empty message: %v", err)
				}
				// Unknown-type errors pair with an UnknownEvent
				// carrier; malformed errors return a nil event. Either
				// is permitted; keep reading until EOF.
				continue
			}
			if ev == nil {
				t.Fatalf("nil event with nil error")
			}
			if ae, ok := ev.(stream.AgentEnd); ok {
				if s := stream.StatusFromAgentEnd(ae); s != stream.StatusDone && s != stream.StatusContinue {
					t.Fatalf("StatusFromAgentEnd returned %v, must be DONE or CONTINUE", s)
				}
			}
		}
	})
}
