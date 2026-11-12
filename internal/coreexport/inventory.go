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
	"sort"
	"strings"

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
	inv := &Inventory{EnvironmentID: env, byAddr: map[string]*Object{}, Skipped: map[string]string{}}
	byType := map[string][]*Object{}
	var defs []*catalog.Def
	for _, d := range catalog.All() {
		if d.Exporter == catalog.ExporterAPI {
			defs = append(defs, d)
		}
	}
	done := map[string]bool{}
	for len(done) < len(defs) {
		progress := false
		for _, d := range defs {
			if done[d.Type] || (d.Parent != "" && !done[d.Parent] && isAPI(d.Parent)) {
				continue
			}
			objs, err := readType(ctx, r, env, d, byType)
			if err != nil {
				// One type the tenant answers unexpectedly must not stop the
				// whole read: it is skipped and reported. Only failures that
				// would hit every type (sign-in, network, cancellation) stop.
				var ae *pingone.APIError
				switch {
				case pingone.Forbidden(err):
					inv.Skipped[d.Type] = "the worker cannot read it (HTTP 403: service not licensed or no read role)"
				case pingone.NotFound(err):
					inv.Skipped[d.Type] = "not available in this environment (HTTP 404)"
				case errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusUnauthorized && ae.Status != http.StatusTooManyRequests:
					// A request this tool builds wrongly for this tenant. A
					// server error (5xx) or throttling that outlasted the
					// retries stops the read instead: skipping would turn
					// every reference to the type into a literal source ID.
					inv.Skipped[d.Type] = fmt.Sprintf("the PingOne API refused the request (HTTP %d); references to these items keep their source IDs, so report it", ae.Status)
				default:
					return nil, fmt.Errorf("coreexport: %s: %w", d.Type, err)
				}
				objs = nil
			}
			byType[d.Type] = objs
			done[d.Type] = true
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("coreexport: the catalog has a parent cycle")
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

func isAPI(typ string) bool {
	d, ok := catalog.Get(typ)
	return ok && d.Exporter == catalog.ExporterAPI
}

func readType(ctx context.Context, r Reader, env string, d *catalog.Def, byType map[string][]*Object) ([]*Object, error) {
	var out []*Object
	add := func(raw map[string]any, parent *Object) {
		if !Cond(d.Match, raw) {
			return
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
			if id == "" {
				continue
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

// Cond evaluates "field=a|b" or "field!=a|b" on an API object. An empty
// condition is true; a missing field compares as "" (and as "false" for
// boolean-looking values).
func Cond(c string, raw map[string]any) bool {
	if c == "" {
		return true
	}
	neg := strings.Contains(c, "!=")
	field, values, _ := strings.Cut(strings.Replace(c, "!=", "=", 1), "=")
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
