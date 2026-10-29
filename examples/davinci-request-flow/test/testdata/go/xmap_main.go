// Mapping-suggestion cross-check (scratch copy only): runs the local app's
// suggestMappings on each fixture and prints the suggested mappings.
package main

import (
	"encoding/json"
	"os"

	"pingone-promote/internal/app"
	"pingone-promote/internal/pingone"
)

type fx struct {
	Name   string             `json:"name"`
	Source []pingone.Resource `json:"source"`
	Target []pingone.Resource `json:"target"`
	Native map[string]string  `json:"native"`
}

func main() {
	var all []fx
	if err := json.NewDecoder(os.Stdin).Decode(&all); err != nil {
		panic(err)
	}
	out := []map[string]string{}
	for _, f := range all {
		out = append(out, app.SuggestMappings(pingone.Inventory{Resources: f.Source}, pingone.Inventory{Resources: f.Target}, f.Native))
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		panic(err)
	}
}
