package promote

import (
	"bytes"
	"fmt"
	"strings"
)

// Op is one line of an edit script.
type Op struct {
	Kind byte // ' ', '-', '+'
	Line string
	// OldLine and NewLine are 1-based line numbers (0 when absent).
	OldLine, NewLine int
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// maxEditDistance bounds the Myers search: the saved frontiers take
// about D*D words, so a larger difference (a whole environment's flows
// replaced) is shown as one replaced block instead.
const maxEditDistance = 2000

// DiffLines computes an edit script: a shortest one (Myers) when the files
// differ in at most maxEditDistance lines; otherwise the part between their
// common prefix and suffix gets one if it can, else it is removed and
// added as one block. Memory stays bounded for files of any size.
func DiffLines(a, b []string) []Op {
	if ops, ok := myers(a, b); ok {
		return ops
	}
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var ops []Op
	for i := 0; i < pre; i++ {
		ops = append(ops, Op{Kind: ' ', Line: a[i], OldLine: i + 1, NewLine: i + 1})
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]
	mid, ok := myers(am, bm)
	if !ok {
		mid = nil
		for i, l := range am {
			mid = append(mid, Op{Kind: '-', Line: l, OldLine: i + 1})
		}
		for j, l := range bm {
			mid = append(mid, Op{Kind: '+', Line: l, NewLine: j + 1})
		}
	}
	for _, op := range mid {
		if op.OldLine > 0 {
			op.OldLine += pre
		}
		if op.NewLine > 0 {
			op.NewLine += pre
		}
		ops = append(ops, op)
	}
	for i := 0; i < suf; i++ {
		oi, ni := len(a)-suf+i, len(b)-suf+i
		ops = append(ops, Op{Kind: ' ', Line: a[oi], OldLine: oi + 1, NewLine: ni + 1})
	}
	return ops
}

// myers is the Myers shortest edit script, O((N+M)D) time; it saves only
// the frontier each step uses (2d+1 entries), and gives up (false) when
// the edit distance exceeds maxEditDistance.
func myers(a, b []string) ([]Op, bool) {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		var ops []Op
		for i, l := range a {
			ops = append(ops, Op{Kind: '-', Line: l, OldLine: i + 1})
		}
		for j, l := range b {
			ops = append(ops, Op{Kind: '+', Line: l, NewLine: j + 1})
		}
		return ops, true
	}
	max := n + m
	limit := max
	if limit > maxEditDistance {
		limit = maxEditDistance
	}
	off := max + 1
	v := make([]int, 2*max+3)
	// trace[d] holds v[off-d .. off+d] after step d.
	var trace [][]int
	final := -1
	for d := 0; d <= limit && final < 0; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				final = d
				break
			}
		}
		trace = append(trace, append([]int(nil), v[off-d:off+d+1]...))
	}
	if final < 0 {
		return nil, false
	}
	at := func(d, k int) int { return trace[d][k+d] }
	var ops []Op
	x, y := n, m
	for d := final; d > 0; d-- {
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && at(d-1, k-1) < at(d-1, k+1)) {
			prevK = k + 1
		}
		prevX := at(d-1, prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			ops = append(ops, Op{Kind: ' ', Line: a[x-1], OldLine: x, NewLine: y})
			x--
			y--
		}
		if x == prevX {
			ops = append(ops, Op{Kind: '+', Line: b[y-1], NewLine: y})
			y--
		} else {
			ops = append(ops, Op{Kind: '-', Line: a[x-1], OldLine: x})
			x--
		}
	}
	for x > 0 && y > 0 {
		ops = append(ops, Op{Kind: ' ', Line: a[x-1], OldLine: x, NewLine: y})
		x--
		y--
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops, true
}

// UnifiedDiff renders a git-style unified diff with three context lines.
// An absent file is nil.
func UnifiedDiff(path string, old, new []byte) string {
	if bytes.Equal(old, new) {
		return ""
	}
	a, b := splitLines(old), splitLines(new)
	ops := DiffLines(a, b)
	var out strings.Builder
	from, to := "a/"+path, "b/"+path
	if old == nil {
		from = "/dev/null"
	}
	if new == nil {
		to = "/dev/null"
	}
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
	const ctx = 3
	for i := 0; i < len(ops); {
		if ops[i].Kind == ' ' {
			i++
			continue
		}
		start := i - ctx
		if start < 0 {
			start = 0
		}
		end := i
		for end < len(ops) {
			if ops[end].Kind != ' ' {
				end++
				continue
			}
			// Look ahead: does another change start within 2*ctx lines?
			j := end
			for j < len(ops) && ops[j].Kind == ' ' && j-end < 2*ctx {
				j++
			}
			if j < len(ops) && ops[j].Kind != ' ' {
				end = j
				continue
			}
			break
		}
		stop := end + ctx
		if stop > len(ops) {
			stop = len(ops)
		}
		oldStart, newStart, oldN, newN := 0, 0, 0, 0
		for _, op := range ops[start:stop] {
			if op.Kind != '+' {
				if oldStart == 0 {
					oldStart = op.OldLine
				}
				oldN++
			}
			if op.Kind != '-' {
				if newStart == 0 {
					newStart = op.NewLine
				}
				newN++
			}
		}
		if oldN == 0 {
			oldStart = lineBefore(ops, start, true)
		}
		if newN == 0 {
			newStart = lineBefore(ops, start, false)
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldN, newStart, newN)
		for _, op := range ops[start:stop] {
			out.WriteByte(op.Kind)
			out.WriteString(op.Line)
			out.WriteByte('\n')
		}
		i = stop
	}
	return out.String()
}

func lineBefore(ops []Op, i int, old bool) int {
	for j := i - 1; j >= 0; j-- {
		if old && ops[j].OldLine > 0 {
			return ops[j].OldLine
		}
		if !old && ops[j].NewLine > 0 {
			return ops[j].NewLine
		}
	}
	return 0
}

// Diff renders the merge's changes as one unified diff, files sorted.
func (r *MergeResult) Diff(head Repo) (string, error) {
	var out strings.Builder
	for _, p := range r.ChangedPaths() {
		old, ok, err := head.Read(p)
		if err != nil {
			return "", err
		}
		if !ok {
			old = nil
		}
		out.WriteString(UnifiedDiff(p, old, r.Files[p]))
	}
	return out.String(), nil
}
