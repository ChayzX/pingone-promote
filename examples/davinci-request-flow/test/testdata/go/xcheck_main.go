// Cross-check harness (scratch copy only): runs the Go functions on fixtures.
package main

import (
	"encoding/json"
	"os"

	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
)

type fixture struct {
	Name     string            `json:"name"`
	Source   pingone.Inventory `json:"source"`
	Target   pingone.Inventory `json:"target"`
	Live     pingone.Inventory `json:"live"`
	Selected []string          `json:"selected"`
	Mappings map[string]string `json:"mappings"`
	Steps    []pingone.Step    `json:"steps"`
	Rollback bool              `json:"rollback"`
	Redact   any               `json:"redact"`
}

type stepOut struct {
	Issue   string   `json:"issue"`
	Changes []string `json:"changes"`
	Name    string   `json:"name"`
	Before  any      `json:"before"`
	After   any      `json:"after"`
}

type result struct {
	Name       string    `json:"name"`
	BuildError string    `json:"buildError"`
	Build      any       `json:"build"`
	Issues     []string  `json:"issues"`
	Steps      []stepOut `json:"steps"`
	Redacted   any       `json:"redacted"`
	Drift      string    `json:"drift"`
}

func main() {
	var fx []fixture
	if err := json.NewDecoder(os.Stdin).Decode(&fx); err != nil {
		panic(err)
	}
	out := []result{}
	for _, f := range fx {
		r := result{Name: f.Name, Redacted: promotion.Redact(f.Redact)}
		b, err := promotion.BuildRequest(f.Source, f.Target, f.Selected, f.Mappings, "d")
		if err != nil {
			r.BuildError = err.Error()
		} else {
			r.Build = b
		}
		for i := range f.Steps {
			if string(f.Steps[i].Payload) == "null" {
				f.Steps[i].Payload = nil
			}
		}
		rev := promotion.Inspect(f.Source, f.Target, f.Selected, f.Mappings, f.Steps, f.Rollback)
		r.Issues = rev.Issues
		for _, s := range rev.Steps {
			if s.Changes == nil {
				s.Changes = []string{}
			}
			r.Steps = append(r.Steps, stepOut{s.Issue, s.Changes, s.Name, s.Before, s.After})
		}
		if f.Live.EnvironmentID != "" {
			a := gitops.Artifact{Request: gitops.Request{Target: gitops.Environment{ID: f.Target.EnvironmentID}, Mappings: f.Mappings}}
			if err := gitops.DriftFromInventories(f.Target, f.Live, f.Steps, a); err != nil {
				r.Drift = err.Error()
			}
		}
		out = append(out, r)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
}
