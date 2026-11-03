package hclblocks

import (
	"strings"
	"testing"
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
