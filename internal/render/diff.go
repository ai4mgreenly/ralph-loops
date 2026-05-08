package render

import "strings"

// diffOp tags one line of a diff as appearing in both inputs, only in
// the original (removed), or only in the replacement (added).
type diffOp int

const (
	diffEqual diffOp = iota
	diffRemove
	diffAdd
)

// diffLine pairs an op with the line text it applies to.
type diffLine struct {
	op   diffOp
	text string
}

// diffLines returns a line-by-line diff of a and b in source order,
// using a longest-common-subsequence (LCS) DP.
//
// The implementation is the textbook O(n·m) LCS table plus a
// backtracking pass; both old/new strings come from a single Edit
// tool call, so n and m are small (typically a handful of lines) and
// the quadratic factor is irrelevant in practice.
//
// Trailing newlines on either input are trimmed before splitting so
// the common "ends with \n" case doesn't produce a spurious blank
// line at the end of the diff. Embedded blank lines are preserved.
func diffLines(a, b string) []diffLine {
	aLines := splitDiffLines(a)
	bLines := splitDiffLines(b)
	n, m := len(aLines), len(bLines)
	if n == 0 && m == 0 {
		return nil
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if aLines[i-1] == bLines[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	var ops []diffLine
	i, j := n, m
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && aLines[i-1] == bLines[j-1]:
			ops = append(ops, diffLine{diffEqual, aLines[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			ops = append(ops, diffLine{diffAdd, bLines[j-1]})
			j--
		default:
			ops = append(ops, diffLine{diffRemove, aLines[i-1]})
			i--
		}
	}
	for x, y := 0, len(ops)-1; x < y; x, y = x+1, y-1 {
		ops[x], ops[y] = ops[y], ops[x]
	}
	return ops
}

// splitDiffLines is the input splitter for [diffLines]. The trailing
// "\n" common in source-file fragments is dropped before splitting so
// the diff doesn't grow a phantom empty trailing line.
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
