package stream_test

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ai4mgreenly/ralph-loops/internal/stream"
)

// ExampleReader_Next walks a small in-memory event flow with the
// concrete-type switch consumers are expected to use. The output line
// per event captures the wire-format discriminator [stream.Event.Kind]
// returns, plus a representative payload field.
func ExampleReader_Next() {
	const input = `{"type":"system","subtype":"init","model":"opus"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}
{"type":"result","is_error":false,"structured_output":{"status":"DONE"}}
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
		case stream.System:
			fmt.Printf("system  model=%s\n", ev.Model)
		case stream.Assistant:
			fmt.Printf("assistant blocks=%d\n", len(ev.Message.Content))
		case stream.Result:
			fmt.Printf("result  is_error=%v\n", ev.IsError)
		}
	}
	// Output:
	// system  model=opus
	// assistant blocks=1
	// result  is_error=false
}
