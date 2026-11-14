package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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

// fromConfig marks a value taken from the server configuration
// (environmentIds), not read from the environment.
const fromConfig = "config"

// autofill enters, for each environment with a known PingOne ID, the
// values a preview asks for that the environment already has: its own
// environment ID, the ID of an existing item, the import ID of one to
// adopt, a DaVinci variable's value or an attribute's value. Only empty
// fields are filled, never secrets; everything filled stays editable and
// is marked. The environments are read at the same time. What happened
// per environment is kept for the page (d.FillReport). It reports whether
// anything was entered.
func (s *Server) autofill(ctx context.Context, d *Draft, res *request.Result, stage func(string)) bool {
	if len(s.cfg.EnvironmentIDs) == 0 && len(s.cfg.SourceEnvironmentIDs) == 0 {
		return false
	}
	envs := append([]string{d.Source}, s.cfg.Rollout...)
	fr, canRead := s.exporter.(factsReader)
	type read struct {
		f   *multiexport.Facts
		err error
	}
	reads := map[string]*read{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var reading []string
	for _, env := range envs {
		id := s.cfg.envID(env)
		if env == d.Source || id == "" || !canRead || !needsFacts(res.Blockers[env]) {
			continue
		}
		reading = append(reading, env)
		wg.Add(1)
		go func(env, id string) {
			defer wg.Done()
			creds := s.creds
			creds.ExportEnvironmentID = id
			f, err := fr.EnvironmentFacts(ctx, creds)
			if err == nil && f == nil {
				err = errors.New("nothing was returned")
			}
			mu.Lock()
			reads[env] = &read{f: f, err: err}
			mu.Unlock()
		}(env, id)
	}
	if len(reading) > 0 {
		stage("Reading what " + strings.Join(reading, ", ") + " already " + map[bool]string{true: "has", false: "have"}[len(reading) == 1] + " (read-only), to fill in values")
		wg.Wait()
	}
	changed := false
	var report []string
	for _, env := range envs {
		bs := res.Blockers[env]
		asked := 0
		for _, b := range bs {
			if b.Code == "missing_value" && b.Var != "" || b.Code == "missing_import" && b.Address != "" {
				asked++
			}
		}
		if asked == 0 {
			continue
		}
		id := s.cfg.envID(env)
		if id == "" {
			report = append(report, env+": nothing filled in, because its PingOne environment ID is not in environmentIds in the server configuration (the name must be exactly "+env+").")
			continue
		}
		r := reads[env]
		if r != nil && r.err != nil {
			s.log.Warn("could not read an environment to fill in values", "env", env, "error", s.redact(r.err.Error()))
			why := s.redact(r.err.Error())
			if errors.Is(r.err, context.DeadlineExceeded) {
				why = "the read took too long"
			}
			report = append(report, env+": could not be read ("+why+"), so nothing but its environment ID can be filled in. Check that the worker has read roles in "+env+".")
		}
		var f *multiexport.Facts
		if r != nil && r.err == nil {
			f = r.f
		}
		s.drafts.mu.Lock()
		if d.FilledAt == nil {
			d.FilledAt = map[string]time.Time{}
		}
		if f != nil {
			d.FilledAt[env] = f.At
		}
		s.drafts.mu.Unlock()
		n := 0
		s.drafts.mu.Lock()
		for _, b := range bs {
			switch {
			case b.Code == "missing_value" && b.Var == neutralize.EnvironmentIDVariable:
				if d.setValue(env, b.Var, id) {
					d.markFilled(env, b.Var, fromConfig)
					n++
				}
			case b.Code == "missing_value" && b.Var != "" && f != nil:
				v := factValue(f, res.Variables[b.Var])
				if v != "" && !looksSecret(b.Var, v) && d.setValue(env, b.Var, v) {
					n++
				}
			case b.Code == "missing_import" && b.Address != "" && f != nil:
				if v := f.ImportIDs[b.Address]; v != "" && d.setImportID(env, b.Address, v) {
					n++
				}
			}
		}
		s.drafts.mu.Unlock()
		if n > 0 {
			changed = true
		}
		if r != nil && r.err != nil {
			continue
		}
		if f != nil && len(f.Raw) == 0 && len(f.Skipped) > 0 {
			report = append(report, fmt.Sprintf("%s: nothing could be read there (%d of %d filled in). The worker probably has no read role in %s, or its ID in environmentIds is wrong.", env, n, asked, env))
			continue
		}
		line := fmt.Sprintf("%s: %d of %d filled in from what %s%s", env, n, asked, env, readAt(d, env))
		if n < asked {
			line += fmt.Sprintf("; %d not found there under the same name, cleared by you, or a secret (never filled in)", asked-n)
		}
		if f != nil && len(f.Skipped) > 0 {
			line += fmt.Sprintf("; %d kinds of items could not be read there (not available in that environment, or no read role for them)", len(f.Skipped))
		}
		if f != nil && f.DaVinci != "" {
			line += "; its DaVinci items could not be read"
		}
		report = append(report, line+".")
	}
	s.drafts.mu.Lock()
	d.FillReport = report
	s.drafts.mu.Unlock()
	return changed
}

// needsFacts reports whether any blocker can be filled from what the
// environment has (beyond its own ID).
func needsFacts(bs []request.BlockerOut) bool {
	for _, b := range bs {
		if b.Code == "missing_value" && b.Var != "" && b.Var != neutralize.EnvironmentIDVariable || b.Code == "missing_import" && b.Address != "" {
			return true
		}
	}
	return false
}

// setValue enters a value the server found, unless one was entered.
func (d *Draft) setValue(env, name, v string) bool {
	if d.Values == nil {
		d.Values = map[string]map[string]string{}
	}
	if d.Values[env] == nil {
		d.Values[env] = map[string]string{}
	}
	if d.Values[env][name] != "" || d.Cleared[env][name] {
		return false
	}
	d.Values[env][name] = v
	d.markFilled(env, name, env)
	return true
}

func (d *Draft) setImportID(env, addr, v string) bool {
	if d.ImportIDs == nil {
		d.ImportIDs = map[string]map[string]string{}
	}
	if d.ImportIDs[env] == nil {
		d.ImportIDs[env] = map[string]string{}
	}
	if d.ImportIDs[env][addr] != "" || d.Cleared[env][addr] {
		return false
	}
	d.ImportIDs[env][addr] = v
	d.markFilled(env, addr, env)
	return true
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

// looksSecret applies request validation's rules (each element of a JSON
// list or object included), so a value is never filled in, shown, and
// then refused.
func looksSecret(name, v string) bool {
	return secretscan.CredentialValue(v) || len(secretscan.ScanText("", []byte(v))) > 0 || request.SecretKind(name, encodeValue(v)) != ""
}

// fillSnapshot is what autofill may change, to take it back.
type fillSnapshot struct {
	values, imports map[string]map[string]string
	filled          filled
}

func copyNested[T any](m map[string]map[string]T) map[string]map[string]T {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]T, len(m))
	for k, in := range m {
		c := make(map[string]T, len(in))
		for k2, v := range in {
			c[k2] = v
		}
		out[k] = c
	}
	return out
}

func (d *Draft) fillState() fillSnapshot {
	return fillSnapshot{values: copyNested(d.Values), imports: copyNested(d.ImportIDs), filled: filled(copyNested(map[string]map[string]string(d.Filled)))}
}

func (d *Draft) restoreFill(s fillSnapshot) {
	d.Values, d.ImportIDs, d.Filled = s.values, s.imports, s.filled
}
