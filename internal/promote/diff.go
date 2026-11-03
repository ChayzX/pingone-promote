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

// DiffLines computes a shortest edit script (Myers, O((N+M)D)).
func DiffLines(a, b []string) []Op {
	n, m := len(a), len(b)
	max := n + m
	off := max + 1
	v := make([]int, 2*max+3)
	var trace [][]int
	final := -1
	for d := 0; d <= max && final < 0; d++ {
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
		trace = append(trace, append([]int(nil), v...))
	}
	var ops []Op
	x, y := n, m
	for d := final; d > 0; d-- {
		vv := trace[d-1]
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && vv[off+k-1] < vv[off+k+1]) {
			prevK = k + 1
		}
		prevX := vv[off+prevK]
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
	return ops
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
