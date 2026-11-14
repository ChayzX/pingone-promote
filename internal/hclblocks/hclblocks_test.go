package hclblocks

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const src = `# leading comment
resource "t" "a" {
  x = 1
}

# comment for b
resource "t" "c" {
  x = 3
}

module "m" {
  source = "./m"
  aa     = var.aa
}
`

func TestReplaceInsertRemoveAreSurgical(t *testing.T) {
	f, err := Load("x.tf", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Replace("resource.t.a", []byte("resource \"t\" \"a\" {\n  x = 10\n}")); err != nil {
		t.Fatal(err)
	}
	if err := f.Insert("resource.t.b", []byte("resource \"t\" \"b\" {\n  x = 2\n}\n"), true); err != nil {
		t.Fatal(err)
	}
	if err := f.AppendToBlock("module.m", []byte("  bb = var.bb\n")); err != nil {
		t.Fatal(err)
	}
	want := `# leading comment
resource "t" "a" {
  x = 10
}

# comment for b
resource "t" "b" {
  x = 2
}

resource "t" "c" {
  x = 3
}

module "m" {
  source = "./m"
  aa     = var.aa

  bb = var.bb
}
`
	if got := string(f.Src); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if err := f.Remove("resource.t.b"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(f.Src), `"b"`) || !strings.Contains(string(f.Src), "# comment for b\nresource \"t\" \"c\"") {
		t.Fatalf("remove: %s", f.Src)
	}
	if err := f.Insert("resource.t.a", []byte("resource \"t\" \"a\" {}"), true); err == nil {
		t.Fatal("duplicate insert must fail")
	}
}

func TestInsertAddsMissingFinalNewline(t *testing.T) {
	f, _ := Load("x.tf", []byte("resource \"t\" \"a\" {\n}"))
	if err := f.Insert("resource.t.z", []byte("resource \"t\" \"z\" {\n}"), true); err != nil {
		t.Fatal(err)
	}
	if string(f.Src) != "resource \"t\" \"a\" {\n}\n\nresource \"t\" \"z\" {\n}\n" {
		t.Fatalf("%q", f.Src)
	}
}

func TestEqualIgnoresAlignment(t *testing.T) {
	if !Equal([]byte("resource \"t\" \"a\" {\n  x = 1\n  yy = 2\n}"), []byte("resource \"t\" \"a\" {\n  x  = 1\n  yy = 2\n}\n")) {
		t.Fatal("alignment must not matter")
	}
}

// Editing by splice gives the same file and blocks as parsing it again.
func TestSpliceMatchesReparse(t *testing.T) {
	src := []byte("# head\nresource \"a\" \"x1\" {\n  v = 1\n}\n\nresource \"a\" \"x3\" {\n  v = 3\n}\n# tail")
	f, err := Load("f.tf", src)
	if err != nil {
		t.Fatal(err)
	}
	steps := []func() error{
		func() error { return f.Insert("resource.a.x2", []byte("resource \"a\" \"x2\" {\n  v = 2\n}"), true) },
		func() error { return f.Insert("resource.a.x9", []byte("resource \"a\" \"x9\" {}"), true) },
		func() error { return f.Replace("resource.a.x1", []byte("resource \"a\" \"x1\" {\n  v = 10\n}")) },
		func() error { return f.AppendText([]byte("import {\n  to = a.x1\n  id = \"1\"\n}")) },
		func() error { return f.AppendText([]byte("k = \"v\"")) },
		func() error { return f.Remove("resource.a.x3") },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		want, err := Scan(f.Src, "f.tf")
		if err != nil {
			t.Fatalf("step %d left invalid HCL: %v\n%s", i, err, f.Src)
		}
		if fmt.Sprint(want) != fmt.Sprint(f.Blocks) {
			t.Fatalf("step %d: blocks %v, a fresh parse gives %v", i, f.Blocks, want)
		}
	}
}

// Inserting many blocks into a large file stays fast (no re-parse each
// time).
func TestManyInsertsStayFast(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "resource \"a\" \"r%05d\" {\n  v = %q\n}\n\n", i*2, strings.Repeat("x", 500))
	}
	f, err := Load("f.tf", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 500; i++ {
		key := fmt.Sprintf("r%05d", i*2+1)
		if err := f.Insert("resource.a."+key, []byte(fmt.Sprintf("resource \"a\" %q {\n  v = 1\n}", key)), true); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("500 inserts took %s", d)
	}
}
