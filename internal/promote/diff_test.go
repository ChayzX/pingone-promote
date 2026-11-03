package promote

import (
	"math/rand"
	"strings"
	"testing"
)

func lcs(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	return dp[0][0]
}

func TestDiffLinesIsMinimalAndReconstructs(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for n := 0; n < 500; n++ {
		gen := func() []string {
			out := make([]string, r.Intn(12))
			for i := range out {
				out[i] = string(rune('a' + r.Intn(4)))
			}
			return out
		}
		a, b := gen(), gen()
		ops := DiffLines(a, b)
		var ra, rb []string
		same := 0
		for _, op := range ops {
			if op.Kind != '+' {
				ra = append(ra, op.Line)
			}
			if op.Kind != '-' {
				rb = append(rb, op.Line)
			}
			if op.Kind == ' ' {
				same++
			}
		}
		if strings.Join(ra, ",") != strings.Join(a, ",") || strings.Join(rb, ",") != strings.Join(b, ",") {
			t.Fatalf("does not reconstruct %v %v: %+v", a, b, ops)
		}
		if same != lcs(a, b) {
			t.Fatalf("not minimal for %v %v", a, b)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	got := UnifiedDiff("f", []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n"), []byte("1\n2\n3\n4\nX\n6\n7\n8\n9\n"))
	want := "--- a/f\n+++ b/f\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+X\n 6\n 7\n 8\n"
	if got != want {
		t.Fatalf("%q", got)
	}
	if UnifiedDiff("f", nil, []byte("a\n")) != "--- /dev/null\n+++ b/f\n@@ -0,0 +1,1 @@\n+a\n" {
		t.Fatal(UnifiedDiff("f", nil, []byte("a\n")))
	}
}
