package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadArtifactRejectsAmbiguousKeys(t *testing.T) {
	_, a := fixture(t)
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string][]byte{
		"duplicate":    append([]byte(`{"kind":"rollback",`), raw[1:]...),
		"case alias":   bytes.Replace(raw, []byte(`"kind"`), []byte(`"KIND"`), 1),
		"nested alias": bytes.Replace(raw, []byte(`"commitSha"`), []byte(`"COMMITSHA"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(p, input, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readArtifact(p); err == nil {
				t.Fatal("ambiguous artifact accepted")
			}
		})
	}
}

func TestRunnerBindsRequestIDToSelectedFolder(t *testing.T) {
	r, _ := fixture(t)
	for _, folder := range []string{r.ID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "unrelated"} {
		t.Run(folder, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "requests", folder, "request.json")
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			putJSON(t, p, r)
			_, err := readRequest(p)
			if (err == nil) != (folder == r.ID) {
				t.Fatalf("request folder binding: accepted=%v matching=%v", err == nil, folder == r.ID)
			}
		})
	}
}
