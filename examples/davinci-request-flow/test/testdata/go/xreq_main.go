// Request cross-check (scratch copy only): Go builds each fixture's request with
// NewRequest+Encode, compares bytes with the JS output, and Decodes the JS bytes.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
)

type fx struct {
	Name         string            `json:"name"`
	Source       pingone.Inventory `json:"source"`
	Target       pingone.Inventory `json:"target"`
	SourceRegion string            `json:"sourceRegion"`
	TargetRegion string            `json:"targetRegion"`
	Selected     []string          `json:"selected"`
	Mappings     map[string]string `json:"mappings"`
	Description  string            `json:"description"`
	ID           string            `json:"id"`
	CreatedAt    string            `json:"createdAt"`
}

type glFx struct {
	Base, Project, Environment, WebURL string
	BranchName, CommitMessage          string
}

type glOut struct {
	ConfigError     bool `json:"configError"`
	Same            bool `json:"same"`
	SubmissionError bool `json:"submissionError"`
}

type jsOut struct {
	Name  string `json:"name"`
	Error string `json:"error"`
	JSON  string `json:"json"`
}

func main() {
	if len(os.Args) == 4 {
		gitlabCheck()
		return
	}
	var fixtures []fx
	var js []jsOut
	f1, _ := os.ReadFile(os.Args[1])
	f2, _ := os.ReadFile(os.Args[2])
	if err := json.Unmarshal(f1, &fixtures); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(f2, &js); err != nil {
		panic(err)
	}
	failures := 0
	for i, f := range fixtures {
		r, err := gitops.NewRequest(f.Source, f.Target, f.SourceRegion, f.TargetRegion, f.Selected, f.Mappings, f.Description)
		var goJSON string
		if err == nil {
			r.ID, r.CreatedAt = f.ID, f.CreatedAt
			var b []byte
			b, err = gitops.Encode(r)
			goJSON = string(b)
		}
		j := js[i]
		switch {
		case err != nil && j.Error == "":
			failures++
			fmt.Printf("MISMATCH %s: Go rejected (%v), JS accepted\n", f.Name, err)
		case err == nil && j.Error != "":
			failures++
			fmt.Printf("MISMATCH %s: Go accepted, JS rejected (%s)\n", f.Name, j.Error)
		case err == nil:
			if goJSON != j.JSON {
				failures++
				fmt.Printf("MISMATCH %s: bytes differ\n--- go\n%s\n--- js\n%s\n", f.Name, goJSON, j.JSON)
			}
			if _, derr := gitops.Decode(bytes.NewReader([]byte(j.JSON + "\n"))); derr != nil {
				failures++
				fmt.Printf("MISMATCH %s: Go Decode rejected JS request: %v\n", f.Name, derr)
			}
		}
	}
	fmt.Printf("%d request fixtures, %d mismatches\n", len(fixtures), failures)
	if failures > 0 {
		os.Exit(1)
	}
}

// gitlabCheck compares config validation and sameGitLabLocation decisions.
func gitlabCheck() {
	var fixtures []glFx
	var js []glOut
	f1, _ := os.ReadFile(os.Args[1])
	f2, _ := os.ReadFile(os.Args[2])
	if json.Unmarshal(f1, &fixtures) != nil || json.Unmarshal(f2, &js) != nil {
		panic("bad gitlab fixtures")
	}
	failures := 0
	for i, f := range fixtures {
		g := glOut{ConfigError: gitlab.XValidate(f.Base, f.Project, f.Environment) != nil}
		if !g.ConfigError {
			g.Same = gitlab.XSameLocation(f.Base, f.WebURL)
		}
		g.SubmissionError = gitlab.XValidateSubmissionOptions(f.BranchName, f.CommitMessage) != nil
		if g != js[i] {
			failures++
			fmt.Printf("MISMATCH gitlab %+v: go %+v js %+v\n", f, g, js[i])
		}
	}
	fmt.Printf("%d GitLab fixtures, %d mismatches\n", len(fixtures), failures)
	if failures > 0 {
		os.Exit(1)
	}
}
