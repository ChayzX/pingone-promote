package tfvars

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// FuzzParse: any accepted input re-renders to a canonical form that is a
// fixed point of Write(Parse(.)).
func FuzzParse(f *testing.F) {
	for name := range goldens {
		if src, err := os.ReadFile(goldenPath(name)); err == nil {
			f.Add(src)
		}
	}
	f.Add([]byte(withFlows(`{ f = { name = "x$${y}%%{z}", tier = 0, graph = { nodes = {}, edges = {} } } }`)))
	f.Fuzz(func(t *testing.T, src []byte) {
		doc, err := Parse(src, "fuzz.tfvars")
		if err != nil {
			return
		}
		out, err := Write(doc)
		if err != nil {
			return // e.g. a JSON field that is not a JSON object
		}
		doc2, err := Parse(out, "fuzz.tfvars")
		if err != nil {
			t.Fatalf("writer output does not parse: %v\n%s", err, out)
		}
		out2, err := Write(doc2)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("round trip is not byte-identical: %v", err)
		}
	})
}

// FuzzStrings: arbitrary strings in plain and JSON fields survive
// Write -> Parse -> Write byte-identically, including ${, %{, quotes,
// backslashes, control characters and non-NFC text.
func FuzzStrings(f *testing.F) {
	f.Add("plain", "json value")
	f.Add("$${a} %{b} ${c}", `"\ quote`)
	f.Add("é", " \x00\x7f")
	f.Add("JSON", "\nJSON\n")
	f.Fuzz(func(t *testing.T, plain, jsonValue string) {
		d := emptyDocument()
		props, err := jsonObjectWith("k", jsonValue)
		if err != nil {
			return
		}
		d.Connections["c"] = Connection{Name: plain, Connector: "x", Properties: &props}
		out, err := Write(d)
		if err != nil {
			return // invalid UTF-8 is rejected by design
		}
		d2, err := Parse(out, "fuzz.tfvars")
		if err != nil {
			t.Fatalf("parse failed: %v\n%s", err, out)
		}
		out2, err := Write(d2)
		if err != nil || !bytes.Equal(out, out2) {
			t.Fatalf("round trip differs: %v", err)
		}
		// JSON content is lossless (non-NFC text is \u-escaped, not normalized).
		if !jsonEqual(*d2.Connections["c"].Properties, props) {
			t.Fatal("JSON value changed across the round trip")
		}
	})
}

func jsonObjectWith(key, value string) (string, error) {
	var b strings.Builder
	b.WriteString("{")
	writeJSONString(&b, key, true)
	b.WriteString(":")
	writeJSONString(&b, value, true)
	b.WriteString("}")
	_, err := CanonicalJSON(b.String())
	return b.String(), err
}

// FuzzCanonicalJSON: canonicalization is idempotent.
func FuzzCanonicalJSON(f *testing.F) {
	f.Add(`{"b": [1, 2.50, {"a": null}], "a": "x"}`)
	f.Add(`{"k": "é é"}`)
	f.Fuzz(func(t *testing.T, s string) {
		c, err := CanonicalJSON(s)
		if err != nil {
			return
		}
		c2, err := CanonicalJSON(c)
		if err != nil || c != c2 {
			t.Fatalf("not idempotent: %q -> %q", c, c2)
		}
	})
}
