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

// BenchmarkReader_Next replays a real pi capture through
// [stream.Reader] one full pass per iteration, then derives the Q3
// status from the terminal [stream.AgentEnd] — the production hot path.
// A [bytes.Reader] is used so the benchmark exercises only the decode
// path, not file I/O. The reported MB/s comes from SetBytes.
func BenchmarkReader_Next(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "tool-edit.jsonl"))
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := stream.NewReader(bytes.NewReader(data))
		for {
			ev, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			// Decode errors (unknown types) are part of the production
			// hot path; drain and keep going.
			if err != nil {
				continue
			}
			if ae, ok := ev.(stream.AgentEnd); ok {
				_ = stream.StatusFromAgentEnd(ae)
			}
		}
	}
}
