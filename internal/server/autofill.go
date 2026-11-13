package server

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/multiexport"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/request"
	"pingone-promote/internal/secretscan"
)

// factsReader reads what an environment already has (multiexport).
type factsReader interface {
	EnvironmentFacts(ctx context.Context, creds exporter.Credentials) (*multiexport.Facts, error)
}

func (p plainErrors) EnvironmentFacts(ctx context.Context, creds exporter.Credentials) (*multiexport.Facts, error) {
	fr, ok := p.inner.(factsReader)
	if !ok {
		return nil, nil
	}
	f, err := fr.EnvironmentFacts(ctx, creds)
	return f, p.wrap("read "+creds.ExportEnvironment(), err)
}

// filled records a value the server entered from what an environment
// already has (env -> name -> where it came from).
type filled map[string]map[string]string

// autofill enters, for each environment with a known PingOne ID, the
// values a preview asks for that the environment already has: its own
// environment ID, the ID of an existing item, the import ID of one to
// adopt, a DaVinci variable's value or an attribute's value. Only empty
// fields are filled, never secrets; everything filled stays editable and
// is marked. It reports whether anything was entered.
func (s *Server) autofill(ctx context.Context, d *Draft, res *request.Result, stage func(string)) bool {
	changed := false
	for _, env := range append([]string{d.Source}, s.cfg.Rollout...) {
		bs := res.Blockers[env]
		id := s.cfg.envID(env)
		if len(bs) == 0 || id == "" {
			continue
		}
		var facts *multiexport.Facts
		load := func() *multiexport.Facts {
			if facts != nil || env == d.Source {
				return facts
			}
			fr, ok := s.exporter.(factsReader)
			if !ok {
				return nil
			}
			stage("Reading what " + env + " already has (read-only), to fill in its values")
			creds := s.creds
			creds.ExportEnvironmentID = id
			f, err := fr.EnvironmentFacts(ctx, creds)
			if err != nil || f == nil {
				s.log.Warn("could not read an environment to fill in values", "env", env, "error", errString(err))
				facts = &multiexport.Facts{}
				return facts
			}
			facts = f
			return facts
		}
		for _, b := range bs {
			var name, value string
			switch {
			case b.Code == "missing_value" && b.Var == neutralize.EnvironmentIDVariable:
				name, value = b.Var, id
			case b.Code == "missing_value" && b.Var != "":
				f := load()
				if f == nil {
					continue
				}
				name, value = b.Var, factValue(f, res.Variables[b.Var])
			case b.Code == "missing_import" && b.Address != "":
				f := load()
				if f == nil || f.ImportIDs[b.Address] == "" {
					continue
				}
				s.drafts.mu.Lock()
				if d.ImportIDs == nil {
					d.ImportIDs = map[string]map[string]string{}
				}
				if d.ImportIDs[env] == nil {
					d.ImportIDs[env] = map[string]string{}
				}
				if d.ImportIDs[env][b.Address] == "" {
					d.ImportIDs[env][b.Address] = f.ImportIDs[b.Address]
					d.markFilled(env, b.Address, env)
					changed = true
				}
				s.drafts.mu.Unlock()
				continue
			}
			if name == "" || value == "" || secretscan.CredentialValue(value) || len(secretscan.ScanText("", []byte(value))) > 0 {
				continue
			}
			s.drafts.mu.Lock()
			if d.Values == nil {
				d.Values = map[string]map[string]string{}
			}
			if d.Values[env] == nil {
				d.Values[env] = map[string]string{}
			}
			if d.Values[env][name] == "" {
				d.Values[env][name] = value
				d.markFilled(env, name, env)
				changed = true
			}
			s.drafts.mu.Unlock()
		}
	}
	return changed
}

func (d *Draft) markFilled(env, key, from string) {
	if d.Filled == nil {
		d.Filled = filled{}
	}
	if d.Filled[env] == nil {
		d.Filled[env] = map[string]string{}
	}
	d.Filled[env][key] = from
}

// factValue is the environment's value for a per-environment variable:
// the ID of an item that is not copied, a DaVinci variable's value, or an
// attribute of the item it belongs to. Lists are JSON, as the form keeps
// them.
func factValue(f *multiexport.Facts, v request.VariableOut) string {
	if v.Resource == "" {
		return ""
	}
	switch {
	case v.Kind == string(neutralize.KindExcludedRef):
		return f.IDs[v.Resource]
	case strings.HasPrefix(v.Resource, neutralize.TypeVariable+".") && v.Attribute == "value":
		return f.Values[v.Resource]
	case v.Attribute != "":
		raw, ok := f.Raw[v.Resource]
		if !ok {
			return ""
		}
		typ, _, _ := strings.Cut(v.Resource, ".")
		x := coreexport.AttributeValue(typ, raw, v.Attribute)
		if x == nil {
			x = lookupAttr(raw, v.Attribute) // DaVinci items follow their API names
		}
		switch t := x.(type) {
		case string:
			return t
		case nil:
			return ""
		case []any:
			if len(t) == 0 {
				return ""
			}
		}
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return ""
}

// lookupAttr follows a Terraform attribute path ("oauth.redirect_uris")
// through an API object, matching names without case or underscores
// (redirect_uris -> redirectUris).
func lookupAttr(raw map[string]any, path string) any {
	var cur any = raw
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		want := strings.ToLower(strings.ReplaceAll(seg, "_", ""))
		var next any
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.ToLower(strings.ReplaceAll(k, "_", "")) == want {
				next = m[k]
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
