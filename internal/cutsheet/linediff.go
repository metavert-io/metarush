package cutsheet

import (
	"fmt"
	"strings"
)

// DiffLine is one line of a line-by-line comparison of two sheets.
type DiffLine struct {
	Op    string `json:"op" jsonschema:"enum=same,enum=added,enum=removed"`
	Text  string `json:"text"`
	Left  int    `json:"left,omitempty"`  // line number in the old sheet
	Right int    `json:"right,omitempty"` // line number in the new sheet
}

// LineDiff compares two texts line by line (longest common subsequence).
func LineDiff(a, b string) []DiffLine {
	al := strings.Split(strings.TrimSuffix(a, "\n"), "\n")
	bl := strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	// Trim the common prefix and suffix first; sheets usually differ in a few lines.
	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	var out []DiffLine
	for i := 0; i < pre; i++ {
		out = append(out, DiffLine{Op: "same", Text: al[i], Left: i + 1, Right: i + 1})
	}
	ma, mb := al[pre:len(al)-suf], bl[pre:len(bl)-suf]
	n, m := len(ma), len(mb)
	if n*m > 4_000_000 { // pathological: show it as a replaced block
		for i, l := range ma {
			out = append(out, DiffLine{Op: "removed", Text: l, Left: pre + i + 1})
		}
		for j, l := range mb {
			out = append(out, DiffLine{Op: "added", Text: l, Right: pre + j + 1})
		}
	} else {
		dp := make([][]int32, n+1)
		for i := range dp {
			dp[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && ma[i] == mb[j]:
				out = append(out, DiffLine{Op: "same", Text: ma[i], Left: pre + i + 1, Right: pre + j + 1})
				i++
				j++
			case j < m && (i == n || dp[i][j+1] >= dp[i+1][j]):
				out = append(out, DiffLine{Op: "added", Text: mb[j], Right: pre + j + 1})
				j++
			default:
				out = append(out, DiffLine{Op: "removed", Text: ma[i], Left: pre + i + 1})
				i++
			}
		}
	}
	for k := 0; k < suf; k++ {
		out = append(out, DiffLine{Op: "same", Text: al[len(al)-suf+k], Left: len(al) - suf + k + 1, Right: len(bl) - suf + k + 1})
	}
	// Show removals before additions within a changed run (reads like git).
	for s := 0; s < len(out); {
		if out[s].Op == "same" {
			s++
			continue
		}
		e := s
		for e < len(out) && out[e].Op != "same" {
			e++
		}
		run := append([]DiffLine(nil), out[s:e]...)
		k := s
		for _, l := range run {
			if l.Op == "removed" {
				out[k] = l
				k++
			}
		}
		for _, l := range run {
			if l.Op == "added" {
				out[k] = l
				k++
			}
		}
		s = e
	}
	return out
}

// Unified renders a diff with a few lines of context around each change, as a
// ```diff block body.
func Unified(lines []DiffLine, context int) string {
	var b strings.Builder
	show := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op != "same" {
			for k := max(0, i-context); k <= min(len(lines)-1, i+context); k++ {
				show[k] = true
			}
		}
	}
	last := -1
	for i, l := range lines {
		if !show[i] {
			continue
		}
		if last >= 0 && i > last+1 {
			b.WriteString("…\n")
		}
		if last < 0 && i > 0 {
			fmt.Fprintf(&b, "@@ line %d @@\n", max(l.Left, l.Right))
		}
		b.WriteString(map[string]string{"same": " ", "added": "+", "removed": "-"}[l.Op])
		b.WriteString(l.Text)
		b.WriteByte('\n')
		last = i
	}
	return b.String()
}

// Changed reports whether the diff has any added or removed line.
func Changed(lines []DiffLine) bool {
	for _, l := range lines {
		if l.Op != "same" {
			return true
		}
	}
	return false
}
