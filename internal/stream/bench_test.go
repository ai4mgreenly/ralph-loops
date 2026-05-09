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

// BenchmarkReader_Next replays the testdata session through
// [stream.Reader] one full pass per iteration. We use a [bytes.Reader]
// so the benchmark exercises only the decode path, not file I/O. The
// reported MB/s comes from the SetBytes call.
func BenchmarkReader_Next(b *testing.B) {
	path := filepath.Join("testdata", "session.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read fixture: %v", err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r := stream.NewReader(bytes.NewReader(data))
		for {
			_, err := r.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			// Decode errors are part of the production hot path
			// (unknown event types are returned with a paired
			// error), so we drain them and keep going.
			if err != nil {
				continue
			}
		}
	}
}
