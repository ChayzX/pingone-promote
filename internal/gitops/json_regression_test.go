package gitops

import (
	"bytes"
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguousRequestKeys(t *testing.T) {
	r := testRequest(t)
	raw, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string][]byte{
		"duplicate":         append([]byte(`{"description":"reviewed",`), bytes.TrimSpace(raw)[1:]...),
		"case alias":        bytes.Replace(raw, []byte(`"description"`), []byte(`"DESCRIPTION"`), 1),
		"case override":     append([]byte(`{"DESCRIPTION":"reviewed",`), bytes.TrimSpace(raw)[1:]...),
		"escaped duplicate": append([]byte(`{"descr\u0069ption":"reviewed",`), bytes.TrimSpace(raw)[1:]...),
		"nested alias":      bytes.Replace(raw, []byte(`"organizationId"`), []byte(`"ORGANIZATIONID"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(bytes.NewReader(input)); err == nil {
				t.Fatal("ambiguous request accepted")
			}
		})
	}
	// Arbitrary configuration maps retain case-sensitive application data keys.
	r.Configurations["flow"] = map[string]any{"name": "Flow", "labels": map[string]any{"Team": "A", "team": "B"}}
	r.Fingerprints["flow"] = fingerprint(r.Configurations["flow"])
	raw, err = Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(strings.NewReader(string(raw))); err != nil {
		t.Fatalf("distinct configuration keys rejected: %v", err)
	}
}
