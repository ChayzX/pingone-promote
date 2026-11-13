// Package coreexport is the native, read-only exporter for core PingOne
// configuration (ADR 0019). It reads one source environment through the
// PingOne Platform API with the read-only worker, and renders the objects a
// request selects as terraformer-shaped HCL (a child module with one
// <type>.tf per resource type, variables.tf, and root import blocks), so
// the existing neutralizer and additive merge treat DaVinci and core
// resources the same way.
//
// How each type is read, labelled and imported comes from internal/catalog;
// which attributes are written comes from the pinned provider schema. Field
// names follow the API's camelCase of each schema attribute unless the
// catalog renames them. These mappings are unverified against a live tenant
// (docs/acceptance/core-export.md).
package coreexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/pingone"
)

// Reader is the read-only API surface (pingone.Client).
type Reader interface {
	Get(ctx context.Context, env, path string) (map[string]any, error)
	List(ctx context.Context, env, path, embedded string) ([]map[string]any, error)
}

// Object is one exportable API object.
type Object struct {
	Type string
	// ID is the object's ID (for derived types, the parent's).
	ID string
	// ImportID is the import_ids value (without the environment prefix).
	ImportID string
	Label    string
	// Name is a plain-language name for search ("Web / Login").
	Name   string
	Parent *Object
	Raw    map[string]any
	// Builtin objects exist in every environment and are adopted.
	Builtin bool
}

// Address is <type>.<label>.
func (o *Object) Address() string { return o.Type + "." + o.Label }

// Inventory is every exportable core object of one environment.
type Inventory struct {
	EnvironmentID string
	Objects       []*Object
	byAddr        map[string]*Object
	// Skipped lists types that could not be read (403: an unlicensed
	// service or a missing read role) with the reason.
	Skipped  map[string]string
	Warnings []string
}

// Object returns the object at an address.
func (inv *Inventory) Object(addr string) *Object { return inv.byAddr[addr] }

// Read reads every core type of the catalog from the environment.
func Read(ctx context.Context, r Reader, env string) (*Inventory, error) {
	// Several types read the same collection (applications, populations,
	// languages, digital wallet applications per application): each is
	// requested once per read.
	r = &memoReader{r: r, lists: map[string][]map[string]any{}, gets: map[string]map[string]any{}}
	inv := &Inventory{EnvironmentID: env, byAddr: map[string]*Object{}, Skipped: map[string]string{}}
	byType := map[string][]*Object{}
	var defs []*catalog.Def
	for _, d := range catalog.All() {
		if d.Exporter == catalog.ExporterAPI {
			defs = append(defs, d)
		}
	}
	// Types are read in rounds: every type whose parent is read runs in the
	// same round, up to readers at a time (the client waits out 429s).
	// Results are applied in catalog order, so the output does not depend
	// on timing.
	done := map[string]bool{}
	for len(done) < len(defs) {
		var ready []*catalog.Def
		for _, d := range defs {
			if !done[d.Type] && (d.Parent == "" || done[d.Parent] || !isAPI(d.Parent)) {
				ready = append(ready, d)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("coreexport: the catalog has a parent cycle")
		}
		type result struct {
			objs     []*Object
			warnings []string
			err      error
		}
		results := make([]result, len(ready))
		rctx, cancel := context.WithCancel(ctx)
		sem := make(chan struct{}, readers)
		var wg sync.WaitGroup
		for i, d := range ready {
			wg.Add(1)
			go func(i int, d *catalog.Def) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-rctx.Done():
					results[i].err = rctx.Err()
					return
				}
				defer func() { <-sem }()
				res := &results[i]
				res.objs, res.err = readType(rctx, r, env, d, byType, func(w string) { res.warnings = append(res.warnings, w) })
				if res.err != nil && fatalRead(res.err) {
					cancel() // the read stops: do not start more requests
				}
			}(i, d)
		}
		wg.Wait()
		cancel()
		for i, d := range ready {
			res := results[i]
			inv.Warnings = append(inv.Warnings, res.warnings...)
			if res.err != nil {
				why, skip := skipReason(res.err)
				if !skip {
					// The first failure that stops the read, in catalog
					// order (others may be cancellations it caused).
					for j := i; j < len(ready); j++ {
						if results[j].err != nil && fatalRead(results[j].err) && !errors.Is(results[j].err, context.Canceled) {
							return nil, fmt.Errorf("coreexport: %s: %w", ready[j].Type, results[j].err)
						}
					}
					return nil, fmt.Errorf("coreexport: %s: %w", d.Type, res.err)
				}
				inv.Skipped[d.Type] = why
				res.objs = nil
			}
			byType[d.Type] = res.objs
			done[d.Type] = true
		}
	}
	for _, d := range defs {
		inv.Objects = append(inv.Objects, byType[d.Type]...)
	}
	if err := inv.assignLabels(); err != nil {
		return nil, err
	}
	sort.Slice(inv.Objects, func(i, j int) bool { return inv.Objects[i].Address() < inv.Objects[j].Address() })
	return inv, nil
}

// memoReader answers repeated requests within one Read from memory. API
// objects are never modified after reading, so sharing them is safe.
type memoReader struct {
	r     Reader
	mu    sync.Mutex
	lists map[string][]map[string]any
	gets  map[string]map[string]any
}

func (m *memoReader) List(ctx context.Context, env, path, embedded string) ([]map[string]any, error) {
	k := path + "|" + embedded
	m.mu.Lock()
	v, ok := m.lists[k]
	m.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := m.r.List(ctx, env, path, embedded)
	if err == nil {
		m.mu.Lock()
		m.lists[k] = v
		m.mu.Unlock()
	}
	return v, err
}

func (m *memoReader) Get(ctx context.Context, env, path string) (map[string]any, error) {
	m.mu.Lock()
	v, ok := m.gets[path]
	m.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := m.r.Get(ctx, env, path)
	if err == nil {
		m.mu.Lock()
		m.gets[path] = v
		m.mu.Unlock()
	}
	return v, err
}

// readers is how many requests a read makes at a time.
const readers = 6

// skipReason says why a type that failed is skipped, or false when the
// failure stops the whole read. One type the tenant answers unexpectedly
// must not stop the whole read: it is skipped and reported. Only failures
// that would hit every type (sign-in, network, cancellation) stop.
func skipReason(err error) (string, bool) {
	var ae *pingone.APIError
	switch {
	case pingone.Forbidden(err):
		return "the worker cannot read it (HTTP 403: service not licensed or no read role)", true
	case pingone.NotFound(err):
		return "not available in this environment (HTTP 404)", true
	case pingone.BadResponse(err):
		// One endpoint answering in another format (XML, ...) must not
		// stop the whole read.
		return "PingOne's answer could not be read (it is not JSON); references to these items keep their source IDs, so report it", true
	case errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusUnauthorized && ae.Status != http.StatusTooManyRequests:
		// A request this tool builds wrongly for this tenant. A server
		// error (5xx) or throttling that outlasted the retries stops the
		// read instead: skipping would turn every reference to the type
		// into a literal source ID.
		return fmt.Sprintf("the PingOne API refused the request (HTTP %d); references to these items keep their source IDs, so report it", ae.Status), true
	}
	return "", false
}

func fatalRead(err error) bool { _, skip := skipReason(err); return !skip }

func isAPI(typ string) bool {
	d, ok := catalog.Get(typ)
	return ok && d.Exporter == catalog.ExporterAPI
}

func readType(ctx context.Context, r Reader, env string, d *catalog.Def, byType map[string][]*Object, warn func(string)) ([]*Object, error) {
	var out []*Object
	add := func(raw map[string]any, parent *Object) {
		if !Cond(d.Match, raw) {
			return
		}
		for _, f := range sortedAllowed(d.Allowed) {
			if bad := disallowed(raw, f, d.Allowed[f]); len(bad) > 0 {
				name, _ := raw["name"].(string)
				warn(fmt.Sprintf("%s %q is not copied: %s holds %s, which the Terraform provider cannot set; copy it by hand", d.Type, name, f, strings.Join(bad, ", ")))
				return
			}
		}
		o := &Object{Type: d.Type, Raw: raw, Parent: parent}
		o.ID, _ = raw["id"].(string)
		if d.Path == "" && parent != nil {
			o.ID = parent.ID
		}
		if d.Singleton {
			// A singleton is the environment's own; some (the credential
			// issuer profile) still have an ID of their own for import.
			if id, _ := raw["id"].(string); id == "" || d.Import == catalog.ImportEnvironment {
				o.ID = env
			}
		}
		// A child of a built-in object is built-in too (the default sign-on
		// policy's actions exist wherever the policy does).
		o.Builtin = (d.Builtin != "" && Cond(d.Builtin, raw)) || (parent != nil && parent.Builtin && d.Owned)
		if d.Import != catalog.ImportEnvironment {
			o.ImportID = importID(d.Import, o)
		}
		out = append(out, o)
	}
	switch {
	case d.Singleton:
		raw, err := r.Get(ctx, env, d.Path)
		if err != nil {
			return nil, err
		}
		add(raw, nil)
	case d.Path == "":
		// Derived from the parent object (agreement_enable, language_update).
		for _, p := range byType[d.Parent] {
			add(p.Raw, p)
		}
	case strings.Contains(d.Path, "{template}"):
		templates, err := r.List(ctx, env, "templates", "templates")
		if err != nil {
			return nil, err
		}
		for _, t := range templates {
			id, _ := t["id"].(string)
			if id == "" || (strings.HasPrefix(d.Match, "template.id") && !Cond(d.Match, map[string]any{"template": map[string]any{"id": id}})) {
				continue // a template whose contents are not copied is not read
			}
			items, err := r.List(ctx, env, strings.ReplaceAll(d.Path, "{template}", id), d.Embedded)
			if err != nil {
				return nil, err
			}
			parent := &Object{Type: "template", ID: id, Raw: map[string]any{"id": id}}
			for _, it := range items {
				add(it, parent)
			}
		}
	case strings.Contains(d.Path, "{schema}"):
		schemas, err := r.List(ctx, env, "schemas", "schemas")
		if err != nil {
			return nil, err
		}
		for _, s := range schemas {
			id, _ := s["id"].(string)
			if id == "" {
				continue
			}
			items, err := r.List(ctx, env, strings.ReplaceAll(d.Path, "{schema}", id), d.Embedded)
			if err != nil {
				return nil, err
			}
			parent := &Object{Type: "schema", ID: id}
			for _, it := range items {
				add(it, parent)
			}
		}
	case d.Parent != "":
		for _, p := range byType[d.Parent] {
			if !Cond(d.ParentMatch, p.Raw) {
				continue
			}
			path := strings.ReplaceAll(d.Path, "{parent}", p.ID)
			if p.Parent != nil {
				path = strings.ReplaceAll(path, "{grandparent}", p.Parent.ID)
			}
			if !strings.Contains(d.Path, "/") || !strings.Contains(d.Path, "{parent}") {
				// A flat collection filtered by the parent (digital wallet apps).
				items, err := r.List(ctx, env, d.Path, d.Embedded)
				if err != nil {
					return nil, err
				}
				for _, it := range items {
					if refID(it, d.ParentAttr) == p.ID {
						add(it, p)
					}
				}
				continue
			}
			if d.Embedded == "" {
				raw, err := r.Get(ctx, env, path)
				if pingone.NotFound(err) {
					continue
				}
				if err != nil {
					return nil, err
				}
				add(raw, p)
				continue
			}
			items, err := r.List(ctx, env, path, d.Embedded)
			if err != nil {
				return nil, err
			}
			for _, it := range items {
				add(it, p)
			}
		}
	default:
		items, err := r.List(ctx, env, d.Path, d.Embedded)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			add(it, nil)
		}
	}
	return out, nil
}

func sortedAllowed(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// disallowed lists the values at a dot path (through objects and lists of
// objects) that are not in allowed.
func disallowed(raw map[string]any, path string, allowed []string) []string {
	var bad []string
	seen := map[string]bool{}
	var walk func(v any, parts []string)
	walk = func(v any, parts []string) {
		switch x := v.(type) {
		case []any:
			for _, it := range x {
				walk(it, parts)
			}
			return
		case map[string]any:
			if len(parts) > 0 {
				walk(x[parts[0]], parts[1:])
			}
			return
		}
		if len(parts) != 0 || v == nil {
			return
		}
		s := str(v)
		if !slices.Contains(allowed, s) && !seen[s] {
			seen[s] = true
			bad = append(bad, s)
		}
	}
	walk(raw, strings.Split(path, "."))
	return bad
}

// refID reads "<attr without _id>.id" or "<camel attr>" from an object.
func refID(raw map[string]any, attr string) string {
	if attr == "" {
		return ""
	}
	base := strings.TrimSuffix(attr, "_id")
	if m, ok := raw[camel(base)].(map[string]any); ok {
		if s, ok := m["id"].(string); ok {
			return s
		}
	}
	s, _ := raw[camel(attr)].(string)
	return s
}

// Cond evaluates "field=a|b", "field!=a|b", "field~regexp" or
// "field!~regexp" on an API object; alternatives joined by "||" hold when
// any does. An empty condition is true; a missing field compares as "" (and
// as "false" for boolean-looking values).
func Cond(c string, raw map[string]any) bool {
	if c == "" {
		return true
	}
	for _, alt := range strings.Split(c, "||") {
		if cond1(alt, raw) {
			return true
		}
	}
	return false
}

var condREs sync.Map // pattern -> *regexp.Regexp

func cond1(c string, raw map[string]any) bool {
	i := strings.IndexAny(c, "=~")
	if i < 0 {
		return false
	}
	neg := i > 0 && c[i-1] == '!'
	field := c[:i]
	if neg {
		field = c[:i-1]
	}
	op, values := c[i], c[i+1:]
	got := Lookup(raw, field)
	s := ""
	switch v := got.(type) {
	case string:
		s = v
	case bool:
		s = fmt.Sprint(v)
	case json.Number:
		s = v.String()
	case nil:
		s = ""
	default:
		s = fmt.Sprint(v)
	}
	match := false
	if op == '~' {
		re, ok := condREs.Load(values)
		if !ok {
			re = regexp.MustCompile(values)
			condREs.Store(values, re)
		}
		return re.(*regexp.Regexp).MatchString(s) != neg
	}
	for _, want := range strings.Split(values, "|") {
		if s == want || (s == "" && want == "false") {
			match = true
		}
	}
	return match != neg
}

// Lookup follows a dot path through nested objects.
func Lookup(raw map[string]any, path string) any {
	var cur any = raw
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return fmt.Sprint(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// assignLabels gives every object a terraformer-style label: the name
// fields (prefixed by the parent's name for child types), sanitized as
// terraformer does. Two objects of one type with the same label get the
// first 8 characters of their ID appended and a warning; labels are
// therefore stable for a given source environment.
func (inv *Inventory) assignLabels() error {
	// A name field that holds another object's ID (a grant's resource.id)
	// uses that object's name, so labels carry no environment-specific IDs.
	own := func(o *Object) string {
		d, _ := catalog.Get(o.Type)
		var keys []string
		for _, f := range d.NameAttrs {
			if s := str(Lookup(o.Raw, f)); s != "" {
				keys = append(keys, s)
			}
		}
		return strings.Join(keys, " ")
	}
	names := map[string]string{}
	for _, o := range inv.Objects {
		if o.ID != "" && names[o.ID] == "" {
			names[o.ID] = own(o)
		}
	}
	parts := func(o *Object) []string {
		d, ok := catalog.Get(o.Type)
		if !ok {
			return []string{o.ID} // a synthetic parent (a notification template)
		}
		var keys []string
		for _, f := range d.NameAttrs {
			s := str(Lookup(o.Raw, f))
			if n, ok := names[s]; ok && n != "" && s != o.ID {
				s = n
			}
			if s != "" {
				keys = append(keys, s)
			}
		}
		if d.Singleton {
			keys = []string{"environment"}
		}
		if len(keys) == 0 && o.Parent != nil && o.Parent.Raw != nil {
			return nil // one per parent: the parent's name is the label
		}
		if len(keys) == 0 {
			keys = []string{o.ID}
		}
		return keys
	}
	var name func(o *Object) []string
	name = func(o *Object) []string {
		own := parts(o)
		if o.Parent != nil && o.Parent.Raw != nil {
			if d, ok := catalog.Get(o.Type); ok && d.Path == "" {
				return name(o.Parent) // derived: same label as its parent
			}
			return append(name(o.Parent), own...)
		}
		return own
	}
	seen := map[string][]*Object{}
	for _, o := range inv.Objects {
		keys := name(o)
		o.Label = neutralize.Label(keys...)
		o.Name = strings.Join(keys, " / ")
		seen[o.Address()] = append(seen[o.Address()], o)
	}
	// Same-named objects: the oldest keeps the plain label, so a label
	// already in Git does not change when a namesake appears; the newer ones
	// get the first 8 characters of their ID.
	collided := make([]string, 0)
	for addr, objs := range seen {
		if len(objs) > 1 {
			collided = append(collided, addr)
		}
	}
	sort.Strings(collided)
	for _, addr := range collided {
		objs := seen[addr]
		sort.SliceStable(objs, func(i, j int) bool {
			ci, cj := str(objs[i].Raw["createdAt"]), str(objs[j].Raw["createdAt"])
			if ci != cj && ci != "" && cj != "" {
				return ci < cj
			}
			return objs[i].ID < objs[j].ID
		})
		for _, o := range objs[1:] {
			short := o.ID
			if len(short) > 8 {
				short = short[:8]
			}
			o.Label = neutralize.Label(append(name(o), short)...)
		}
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("%d objects share the label %s; the oldest keeps it, the others end with the first 8 characters of their IDs", len(objs), addr))
	}
	for _, o := range inv.Objects {
		if inv.byAddr[o.Address()] != nil {
			return &neutralize.CollisionError{What: "label", Name: o.Address(), Addresses: []string{o.Address()}, Hint: "two objects still share a label after adding their ID"}
		}
		inv.byAddr[o.Address()] = o
	}
	return nil
}

func camel(snake string) string {
	parts := strings.Split(snake, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// importID fills an import template ({id}, {parent}, {grandparent}).
func importID(tmpl string, o *Object) string {
	if tmpl == "" {
		tmpl = catalog.ImportID
	}
	parent, grand := "", ""
	if o.Parent != nil {
		parent = o.Parent.ID
		if o.Parent.Parent != nil {
			grand = o.Parent.Parent.ID
		}
	}
	return strings.NewReplacer("{id}", o.ID, "{parent}", parent, "{grandparent}", grand).Replace(tmpl)
}
