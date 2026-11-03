package pipelinesecrets

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "customer_secrets.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseFixture(t *testing.T) {
	f, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range f.Entries() {
		names = append(names, e.VarName)
	}
	if !reflect.DeepEqual(names, []string{"client_secret", "davinci_variable_ExampleAuthNCreds_value", "PingOne_DaVinci_connection_client"}) {
		t.Fatalf("entries %v", names)
	}
	// The variable name and the Vault field can differ.
	e, _ := f.Lookup("PingOne_DaVinci_connection_client")
	if e.Field != "PingOne_DaVinci_connection_client_secret" || e.Mount != "secret" || e.VaultPath != "apps/pingone-davinci/${VAULT_PATH_ENV}/pingone" || e.File == nil || *e.File {
		t.Fatalf("entry %+v", e)
	}
	if !reflect.DeepEqual(f.Commented(), []string{"TF_VAR_basicAuthCredDV"}) {
		t.Fatalf("commented %v", f.Commented())
	}
	if _, ok := f.Lookup("basicAuthCredDV"); ok {
		t.Fatal("a commented-out entry counts as declared")
	}
}

func TestNoChangeRoundTripIsByteIdentical(t *testing.T) {
	src := fixture(t)
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.Add(DefaultConfig, []NewEntry{{VarName: "client_secret"}}) // already declared
	if err != nil || !bytes.Equal(out, src) {
		t.Fatalf("no-op edit changed the file: %v", err)
	}
	out, err = f.Add(DefaultConfig, nil)
	if err != nil || !bytes.Equal(out, src) {
		t.Fatal("empty edit changed the file")
	}
}

func TestAddEntryIsMinimalAndGolden(t *testing.T) {
	src := fixture(t)
	f, _ := Parse(src)
	out, err := f.Add(DefaultConfig, []NewEntry{
		{VarName: "PingOne_DaVinci_connection_PingOne_clientSecret"},
		{VarName: "davinci_variable_apiToken_value", Field: "dv_api_token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "customer_secrets_added.yml")
	if *update {
		os.WriteFile(golden, out, 0o644)
	}
	want, _ := os.ReadFile(golden)
	if !bytes.Equal(out, want) {
		t.Fatalf("differs from %s:\n%s", golden, out)
	}
	// Minimal diff: the original lines are all still there, in order, and the
	// only new lines are the two entries plus their separators.
	oldLines, newLines := strings.Split(string(src), "\n"), strings.Split(string(out), "\n")
	added := diffAdded(oldLines, newLines)
	// (The greedy in-order match pairs the existing blank line first, so the
	// separator blank lines show up after each new entry.)
	wantAdded := []string{
		"    TF_VAR_PingOne_DaVinci_connection_PingOne_clientSecret:",
		"      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/PingOne_DaVinci_connection_PingOne_clientSecret@secret",
		"      file: false", "",
		"    TF_VAR_davinci_variable_apiToken_value:",
		"      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/dv_api_token@secret",
		"      file: false", ""}
	if !reflect.DeepEqual(added, wantAdded) {
		t.Fatalf("added lines:\n%q", added)
	}
	g, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := g.Lookup("davinci_variable_apiToken_value"); !ok || e.Field != "dv_api_token" {
		t.Fatal("new entry not readable")
	}
	if !reflect.DeepEqual(g.Commented(), []string{"TF_VAR_basicAuthCredDV"}) {
		t.Fatal("commented entry was disturbed")
	}
}

// diffAdded returns the lines of b that are not part of a in-order match of a.
func diffAdded(a, b []string) []string {
	var out []string
	i := 0
	for _, line := range b {
		if i < len(a) && a[i] == line {
			i++
			continue
		}
		out = append(out, line)
	}
	if i != len(a) {
		return []string{"<original lines missing>"}
	}
	return out
}

func TestCheckReport(t *testing.T) {
	f, _ := Parse(fixture(t))
	r := f.Check(DefaultConfig, []string{"PingOne_DaVinci_connection_client", "basicAuthCredDV", "brand_new_secret"})
	if r.OK() {
		t.Fatal("report OK with undeclared secrets")
	}
	if !reflect.DeepEqual(r.Declared, []string{"PingOne_DaVinci_connection_client"}) ||
		!reflect.DeepEqual(r.CommentedOut, []string{"basicAuthCredDV"}) ||
		!reflect.DeepEqual(r.Undeclared, []string{"brand_new_secret"}) ||
		!reflect.DeepEqual(r.Unused, []string{"client_secret", "davinci_variable_ExampleAuthNCreds_value"}) ||
		len(r.PatternMismatch) != 0 || len(r.Candidates) != 3 {
		t.Fatalf("report %+v", r)
	}
	// A hand-made exception is only a warning.
	odd := strings.Replace(string(fixture(t)), "pingone/client_secret@secret", "other/client_secret@kv", 1)
	g, _ := Parse([]byte(odd))
	if r := g.Check(DefaultConfig, []string{"client_secret"}); !r.OK() || !reflect.DeepEqual(r.PatternMismatch, []string{"client_secret"}) {
		t.Fatalf("report %+v", r)
	}
}

func TestAddRefusals(t *testing.T) {
	f, _ := Parse(fixture(t))
	var ee *EditError
	if _, err := f.Add(DefaultConfig, []NewEntry{{VarName: "basicAuthCredDV"}}); !errors.As(err, &ee) {
		t.Fatalf("adding a commented-out name: %v", err)
	}
	if _, err := f.Add(DefaultConfig, []NewEntry{{VarName: "bad-name"}}); !errors.As(err, &ee) {
		t.Fatal("invalid name accepted")
	}
	if _, err := f.Add(DefaultConfig, []NewEntry{{VarName: "ok_name", Field: "bad field"}}); !errors.As(err, &ee) {
		t.Fatal("invalid field accepted")
	}
	if _, err := f.Add(Config{PathPrefix: "a: b", Mount: "secret"}, []NewEntry{{VarName: "ok_name"}}); err == nil {
		t.Fatal("unsafe prefix accepted")
	}
}

func TestLayouts(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"empty block": {
			".customer-secrets:\n  secrets:\nother: 1\n",
			".customer-secrets:\n  secrets:\n    TF_VAR_k:\n      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/k@secret\n      file: false\nother: 1\n",
		},
		"no blank lines, four-space indent": {
			".customer-secrets:\n    secrets:\n        TF_VAR_a:\n            vault: x/a@secret\n        TF_VAR_b:\n            vault: x/b@secret\n",
			".customer-secrets:\n    secrets:\n        TF_VAR_a:\n            vault: x/a@secret\n        TF_VAR_b:\n            vault: x/b@secret\n        TF_VAR_k:\n            vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/k@secret\n            file: false\n",
		},
		"crlf": {
			".customer-secrets:\r\n  secrets:\r\n    TF_VAR_a:\r\n      vault: x/a@secret\r\n",
			".customer-secrets:\r\n  secrets:\r\n    TF_VAR_a:\r\n      vault: x/a@secret\r\n\r\n    TF_VAR_k:\r\n      vault: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/k@secret\r\n      file: false\r\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			out, err := f.Add(DefaultConfig, []NewEntry{{VarName: "k"}})
			if err != nil || string(out) != tc.want {
				t.Fatalf("err=%v\n%q\nwant\n%q", err, out, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "- a\n", "other: 1\n", ".customer-secrets:\n  secrets: {TF_VAR_a: {vault: x}}\n", ".customer-secrets:\n  secrets:\n    TF_VAR_a: {}\n    TF_VAR_a: {}\n"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestSplitVault(t *testing.T) {
	for in, want := range map[string][3]string{
		"apps/x/${VAULT_PATH_ENV}/pingone/field@secret": {"apps/x/${VAULT_PATH_ENV}/pingone", "field", "secret"},
		"a/b@c@d":    {"a/b@c", "", ""},
		"plainfield": {"", "plainfield", ""},
	} {
		p, f, m := SplitVault(in)
		_ = want
		if in == "a/b@c@d" {
			if p != "a" || f != "b@c" || m != "d" {
				t.Fatalf("%q -> %q %q %q", in, p, f, m)
			}
			continue
		}
		if [3]string{p, f, m} != want {
			t.Fatalf("%q -> %q %q %q", in, p, f, m)
		}
	}
}

func FuzzParseAdd(f *testing.F) {
	f.Add(string(mustRead("testdata/customer_secrets.yml")), "new_key")
	f.Fuzz(func(t *testing.T, src, name string) {
		file, err := Parse([]byte(src))
		if err != nil {
			return
		}
		out, err := file.Add(DefaultConfig, []NewEntry{{VarName: name}})
		if err != nil {
			return
		}
		if _, err := Parse(out); err != nil {
			t.Fatalf("edit produced unparsable YAML: %v", err)
		}
		if !bytes.Contains(out, []byte(src[:min(len(src), 1)])) {
			t.Fatal("lost content")
		}
	})
}

func mustRead(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}
