package stream_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// ExampleParseStatus shows the sentinel-label → typed-[stream.Status]
// round trip.
func ExampleParseStatus() {
	for _, label := range []string{"DONE", "CONTINUE", "WAT", ""} {
		s, ok := stream.ParseStatus(label)
		fmt.Printf("%-8s ok=%-5v status=%q\n", label, ok, s.String())
	}
	// Output:
	// DONE     ok=true  status="DONE"
	// CONTINUE ok=true  status="CONTINUE"
	// WAT      ok=false status=""
	//          ok=false status=""
}

// ExampleReader_Next walks a small in-memory pi event flow with the
// concrete-type switch consumers are expected to use. The line per
// event shows the wire-format discriminator [stream.Event.Kind]
// returns, plus a representative payload field.
func ExampleReader_Next() {
	const input = `{"type":"session","version":3,"id":"abc","timestamp":"2026-05-17T00:00:00Z","cwd":"/work"}
{"type":"message_start","message":{"role":"assistant"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"done\nRALPH-STATUS: DONE"}],"provider":"openai-codex","model":"gpt-5.3-codex","usage":{"input":10,"output":3,"totalTokens":13,"cost":{"total":0.001}},"stopReason":"stop"}}
{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"done\nRALPH-STATUS: DONE"}]}]}
`
	r := stream.NewReader(strings.NewReader(input))
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Println("err:", err)
			return
		}
		switch ev := ev.(type) {
		case stream.Session:
			fmt.Printf("session cwd=%s\n", ev.Cwd)
		case stream.MessageEnd:
			fmt.Printf("message_end role=%s provider=%s\n", ev.Message.Role, ev.Message.Provider)
		case stream.AgentEnd:
			fmt.Printf("agent_end status=%s\n", stream.StatusFromAgentEnd(ev))
		case stream.KnownEvent:
			fmt.Printf("known   %s\n", ev.Kind())
		}
	}
	// Output:
	// session cwd=/work
	// known   message_start
	// message_end role=assistant provider=openai-codex
	// agent_end status=DONE
}

// ExampleStatusFromAgentEnd derives the terminal control signal from a
// real captured pi run (testdata/continue.jsonl) by draining to the
// terminal [stream.AgentEnd] and applying the Q3 sentinel parser.
func ExampleStatusFromAgentEnd() {
	data, err := os.ReadFile(filepath.Join("testdata", "continue.jsonl"))
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	r := stream.NewReader(bytes.NewReader(data))
	var status = stream.StatusContinue
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			continue // unknown/malformed lines are recoverable
		}
		if ae, ok := ev.(stream.AgentEnd); ok {
			status = stream.StatusFromAgentEnd(ae)
		}
	}
	fmt.Println(status)
	// Output:
	// CONTINUE
}
