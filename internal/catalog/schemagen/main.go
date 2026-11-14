// Command schemagen trims `terraform providers schema -json` for the pinned
// pingidentity/pingone provider into internal/catalog/provider_schema.json:
// only resource types, their attribute trees and the flags the exporter
// needs. Run it after changing the provider pin (ADR 0019):
//
//	cd $(mktemp -d)
//	printf 'terraform {\n  required_providers {\n    pingone = { source = "pingidentity/pingone", version = "1.24.0" }\n  }\n}\n' > main.tf
//	terraform init -input=false && terraform providers schema -json > schema.json
//	go run ./internal/catalog/schemagen -in schema.json -version 1.24.0 > internal/catalog/provider_schema.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
)

type rawAttr struct {
	Type       json.RawMessage `json:"type"`
	NestedType *struct {
		Attributes  map[string]rawAttr `json:"attributes"`
		NestingMode string             `json:"nesting_mode"`
	} `json:"nested_type"`
	Required  bool `json:"required"`
	Optional  bool `json:"optional"`
	Computed  bool `json:"computed"`
	Sensitive bool `json:"sensitive"`
	WriteOnly bool `json:"write_only"`
}

type rawBlock struct {
	Attributes map[string]rawAttr `json:"attributes"`
	BlockTypes map[string]struct {
		NestingMode string   `json:"nesting_mode"`
		Block       rawBlock `json:"block"`
		MinItems    int      `json:"min_items"`
		MaxItems    int      `json:"max_items"`
	} `json:"block_types"`
}

// Attr is the trimmed attribute.
type Attr struct {
	Type      json.RawMessage `json:"type,omitempty"`
	Nesting   string          `json:"nesting,omitempty"` // single, list, set, map (nested attributes or blocks)
	Block     bool            `json:"block,omitempty"`   // a nested block, not a nested attribute
	Attrs     map[string]Attr `json:"attrs,omitempty"`
	Required  bool            `json:"required,omitempty"`
	Optional  bool            `json:"optional,omitempty"`
	Computed  bool            `json:"computed,omitempty"`
	Sensitive bool            `json:"sensitive,omitempty"`
	WriteOnly bool            `json:"writeOnly,omitempty"`
}

// Schema is the trimmed provider schema.
type Schema struct {
	Provider  string                     `json:"provider"`
	Version   string                     `json:"version"`
	Resources map[string]map[string]Attr `json:"resources"`
}

func attrs(in map[string]rawAttr) map[string]Attr {
	out := map[string]Attr{}
	for n, a := range in {
		t := Attr{Type: a.Type, Required: a.Required, Optional: a.Optional, Computed: a.Computed, Sensitive: a.Sensitive, WriteOnly: a.WriteOnly}
		if a.NestedType != nil {
			t.Type = nil
			t.Nesting = a.NestedType.NestingMode
			t.Attrs = attrs(a.NestedType.Attributes)
		}
		out[n] = t
	}
	return out
}

func block(b rawBlock) map[string]Attr {
	out := attrs(b.Attributes)
	for n, bt := range b.BlockTypes {
		out[n] = Attr{Nesting: bt.NestingMode, Block: true, Attrs: block(bt.Block), Required: bt.MinItems > 0, Optional: bt.MinItems == 0}
	}
	return out
}

func main() {
	in := flag.String("in", "", "terraform providers schema -json output")
	version := flag.String("version", "", "provider version")
	flag.Parse()
	raw, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var doc struct {
		ProviderSchemas map[string]struct {
			ResourceSchemas map[string]struct {
				Block rawBlock `json:"block"`
			} `json:"resource_schemas"`
		} `json:"provider_schemas"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := Schema{Version: *version, Resources: map[string]map[string]Attr{}}
	names := make([]string, 0, len(doc.ProviderSchemas))
	for n := range doc.ProviderSchemas {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) != 1 {
		fmt.Fprintln(os.Stderr, "expected exactly one provider in the schema")
		os.Exit(1)
	}
	out.Provider = names[0]
	for t, r := range doc.ProviderSchemas[names[0]].ResourceSchemas {
		out.Resources[t] = block(r.Block)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
