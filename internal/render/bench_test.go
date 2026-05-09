package render

import (
	"strings"
	"testing"
)

// BenchmarkBalanceSGRPerLine guards the per-line ANSI-balancing pass
// the highlighter funnels its terminal-256 output through. Input is
// representative chroma output: each line carries a foreground colour
// plus an end-of-line reset; [balanceSGRPerLine] should stream through
// these without buffering the whole document.
func BenchmarkBalanceSGRPerLine(b *testing.B) {
	const sample = "\x1b[38;5;111mfunc\x1b[0m \x1b[38;5;208mFoo\x1b[0m() {"
	lines := make([]string, 64)
	for i := range lines {
		lines[i] = sample
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = balanceSGRPerLine(lines)
	}
}

// BenchmarkHighlightLines exercises the chroma path on a small Go
// source fragment. The benchmark uses 50 logical lines so the
// per-call cost includes the lexer's typical state-machine work, not
// just the trivial first-token startup.
func BenchmarkHighlightLines(b *testing.B) {
	const stanza = `package main

import "fmt"

func main() {
	fmt.Println("hi")
}
`
	body := strings.Repeat(stanza, 8) // ~50 lines
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = highlightLines("main.go", body, true)
	}
}
