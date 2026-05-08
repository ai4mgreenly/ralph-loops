package loop

import "testing"

func TestDiffLines(t *testing.T) {
	tests := []struct {
		name string
		old  string
		new  string
		want []diffLine
	}{
		{
			name: "identical strings produce equal-only ops",
			old:  "a\nb\nc",
			new:  "a\nb\nc",
			want: []diffLine{
				{diffEqual, "a"},
				{diffEqual, "b"},
				{diffEqual, "c"},
			},
		},
		{
			name: "single line replacement preserves surrounding context",
			old:  "a\nb\nc",
			new:  "a\nB\nc",
			want: []diffLine{
				{diffEqual, "a"},
				{diffRemove, "b"},
				{diffAdd, "B"},
				{diffEqual, "c"},
			},
		},
		{
			name: "pure insertion",
			old:  "",
			new:  "x\ny",
			want: []diffLine{
				{diffAdd, "x"},
				{diffAdd, "y"},
			},
		},
		{
			name: "pure deletion",
			old:  "x\ny",
			new:  "",
			want: []diffLine{
				{diffRemove, "x"},
				{diffRemove, "y"},
			},
		},
		{
			name: "trailing newline does not create phantom blank line",
			old:  "a\nb\n",
			new:  "a\nB\n",
			want: []diffLine{
				{diffEqual, "a"},
				{diffRemove, "b"},
				{diffAdd, "B"},
			},
		},
		{
			name: "both empty returns nil",
			old:  "",
			new:  "",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffLines(tc.old, tc.new)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d, len(want)=%d\ngot:  %v\nwant: %v", len(got), len(tc.want), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("op %d: got %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
