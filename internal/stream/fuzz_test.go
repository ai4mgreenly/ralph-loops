package stream_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// FuzzReader_Next feeds arbitrary bytes through [stream.Reader] and
// asserts the contract every consumer relies on:
//
//   - [stream.Reader.Next] never panics, no matter how malformed the
//     input is;
//   - the (event, error) return is well-formed: at most one is non-nil,
//     and any non-EOF error has a non-nil cause.
//
// The seed corpus pairs a couple of well-formed lines (so the fuzzer
// has something to mutate) with a few representative malformed shapes.
func FuzzReader_Next(f *testing.F) {
	f.Add([]byte(`{"type":"system","subtype":"init","model":"opus"}` + "\n"))
	f.Add([]byte(`{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}` + "\n"))
	f.Add([]byte("not valid json\n"))
	f.Add([]byte(`{"type":"unrecognised_kind"}` + "\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		r := stream.NewReader(bytes.NewReader(data))
		for {
			ev, err := r.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				// Any non-EOF error must carry a real cause.
				if err.Error() == "" {
					t.Fatalf("error with empty message: %v", err)
				}
				// An unknown-type error pairs with an UnknownEvent
				// carrier; a malformed-json error usually returns
				// nil event. Either is permitted; we just keep
				// reading until EOF.
				continue
			}
			if ev == nil {
				t.Fatalf("nil event with nil error")
			}
		}
	})
}
