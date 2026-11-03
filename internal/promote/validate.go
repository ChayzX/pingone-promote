package promote

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/secretscan"
	"pingone-promote/internal/tfvars"
)

// ValidateOverlays checks every environment's overlay against the root
// variables of a snapshot:
//   - each required (non-secret, no default) root variable is set
//     (missing ones are blockers for that environment);
//   - no key is undeclared, and no key is a secret;
//   - import_ids keys are module.<name>.<address> of managed resources;
//   - no value looks like a secret.
//
// envs is the source followed by the rollout; overlays for other
// environments found in the snapshot are validated too.
func ValidateOverlays(r Repo, l Layout, envs []string) ([]Blocker, error) {
	ix, err := loadIndex(r, l)
	if err != nil {
		return nil, err
	}
	found, err := overlayFiles(r, l)
	if err != nil {
		return nil, err
	}
	all := map[string]bool{}
	for _, e := range envs {
		all[e] = true
	}
	for e := range found {
		all[e] = true
	}
	names := make([]string, 0, len(all))
	for e := range all {
		names = append(names, e)
	}
	sort.Strings(names)
	required := ix.requiredOverlayVars()
	secrets := ix.secretVars()
	managed := map[string]bool{}
	for a := range ix.defs {
		managed[l.ModuleAddress(a)] = true
	}
	var out []Blocker
	for _, env := range names {
		p, err := l.Overlay(env)
		if err != nil {
			return nil, err
		}
		src, ok, err := r.Read(p)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, Blocker{Env: env, Code: BlockMissingOverlay, Message: p + " does not exist; it must set " + strings.Join(required, ", ")})
			continue
		}
		o, err := tfvars.ParseOverlay(src, p)
		if err != nil {
			out = append(out, Blocker{Env: env, Code: BlockUndeclaredKey, Message: err.Error()})
			continue
		}
		for _, v := range required {
			if _, ok := o.Values[v]; !ok {
				out = append(out, Blocker{Env: env, Code: BlockMissingValue, Variable: v, Message: p + " must set " + v})
			}
		}
		for _, k := range o.Keys() {
			v := o.Values[k]
			switch _, declared := ix.rootVars[k]; {
			case secrets[k]:
				out = append(out, Blocker{Env: env, Code: BlockSecretInOverlay, Variable: k, Message: k + " is a secret; set it through customer_secrets.yml, never in " + p})
				continue
			case !declared:
				out = append(out, Blocker{Env: env, Code: BlockUndeclaredKey, Variable: k, Message: k + " is not a root variable (" + p + fmt.Sprintf(":%d)", o.Lines[k])})
				continue
			}
			if k == "import_ids" && v.CanIterateElements() {
				for it := v.ElementIterator(); it.Next(); {
					key, val := it.Element()
					if !managed[key.AsString()] {
						out = append(out, Blocker{Env: env, Code: BlockImportKey, Variable: "import_ids", Address: key.AsString(),
							Message: "import_ids key " + key.AsString() + " is not a managed address (Terraform ignores it silently)"})
					}
					if val.Type() != cty.String || val.IsNull() || strings.TrimSpace(val.AsString()) == "" {
						out = append(out, Blocker{Env: env, Code: BlockImportKey, Variable: "import_ids", Address: key.AsString(), Message: "import ID must be a non-empty string"})
					}
				}
			}
			for _, f := range scanValue(k, v) {
				out = append(out, Blocker{Env: env, Code: BlockSecretFinding, Variable: k, Message: "value looks like a secret (" + f + ")"})
			}
		}
	}
	sortBlockers(out)
	return out, nil
}

func scanValue(name string, v cty.Value) []string {
	var kinds []string
	var walk func(cty.Value)
	walk = func(v cty.Value) {
		if v.IsNull() || !v.IsKnown() {
			return
		}
		switch {
		case v.Type() == cty.String:
			s := v.AsString()
			for _, f := range secretscan.ScanText("", []byte(s)) {
				kinds = append(kinds, f.Kind)
			}
			if secretscan.SuspectLiteral(name, s) {
				kinds = append(kinds, secretscan.KindNamed)
			}
		case v.CanIterateElements():
			for it := v.ElementIterator(); it.Next(); {
				_, e := it.Element()
				walk(e)
			}
		}
	}
	walk(v)
	return kinds
}
