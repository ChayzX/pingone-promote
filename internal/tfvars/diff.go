package tfvars

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
)

// ChangeKind classifies one entry of a Diff.
type ChangeKind string

const (
	Added    ChangeKind = "added"
	Removed  ChangeKind = "removed"
	Modified ChangeKind = "modified"
)

// Change is one changed key in one collection. Fields lists the changed
// attribute paths inside the entry (JSON fields continue with /key/...).
// Changes carry paths only, never values, so they are safe for merge
// request descriptions.
type Change struct {
	Collection string     `json:"collection"`
	Key        string     `json:"key,omitempty"`
	Kind       ChangeKind `json:"kind"`
	Fields     []string   `json:"fields,omitempty"`
}

// Diff returns the per-key structural changes from old to new, sorted by
// collection then key. JSON heredocs compare semantically, sets compare as
// sets, and absent equals null.
func Diff(oldDoc, newDoc *Document) []Change {
	var out []Change
	if oldDoc.SchemaVersion != newDoc.SchemaVersion {
		out = append(out, Change{Collection: "tfvars_schema_version", Kind: Modified})
	}
	if oldDoc.EnvironmentID != newDoc.EnvironmentID {
		out = append(out, Change{Collection: "pingone_environment_id", Kind: Modified})
	}
	ov, nv := reflect.ValueOf(oldDoc).Elem(), reflect.ValueOf(newDoc).Elem()
	for _, f := range fieldsOf(ov.Type())[2:] {
		a, b := ov.Field(f.index), nv.Field(f.index)
		if a.Kind() == reflect.Struct { // import_ids
			for _, sf := range fieldsOf(a.Type()) {
				out = append(out, diffMap(f.name+"."+sf.name, a.Field(sf.index), b.Field(sf.index))...)
			}
			continue
		}
		out = append(out, diffMap(f.name, a, b)...)
	}
	return out
}

func diffMap(collection string, a, b reflect.Value) []Change {
	keys := map[string]bool{}
	for _, m := range []reflect.Value{a, b} {
		for _, k := range m.MapKeys() {
			keys[k.String()] = true
		}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []Change
	for _, k := range sorted {
		kv := reflect.ValueOf(k)
		av, bv := a.MapIndex(kv), b.MapIndex(kv)
		switch {
		case !av.IsValid():
			out = append(out, Change{Collection: collection, Key: k, Kind: Added})
		case !bv.IsValid():
			out = append(out, Change{Collection: collection, Key: k, Kind: Removed})
		default:
			var fields []string
			diffValue(av, bv, field{}, "", &fields)
			if len(fields) > 0 {
				out = append(out, Change{Collection: collection, Key: k, Kind: Modified, Fields: fields})
			}
		}
	}
	return out
}

func diffValue(a, b reflect.Value, f field, path string, out *[]string) {
	mark := func() {
		p := path
		if p == "" {
			p = "."
		}
		*out = append(*out, p)
	}
	if a.Kind() == reflect.Pointer || a.Kind() == reflect.Slice || a.Kind() == reflect.Map {
		if a.IsNil() || b.IsNil() {
			if a.IsNil() != b.IsNil() {
				mark()
			}
			return
		}
	}
	switch a.Kind() {
	case reflect.Pointer:
		diffValue(a.Elem(), b.Elem(), f, path, out)
	case reflect.String:
		if f.json {
			diffJSON(a.String(), b.String(), path, out)
		} else if a.String() != b.String() {
			mark()
		}
	case reflect.Slice:
		if f.set {
			as, bs := make([]string, a.Len()), make([]string, b.Len())
			for i := range as {
				as[i] = a.Index(i).String()
			}
			for i := range bs {
				bs[i] = b.Index(i).String()
			}
			if !reflect.DeepEqual(sortedUnique(as), sortedUnique(bs)) {
				mark()
			}
			return
		}
		if a.Len() != b.Len() {
			mark()
			return
		}
		for i := 0; i < a.Len(); i++ {
			diffValue(a.Index(i), b.Index(i), field{}, path+"["+strconv.Itoa(i)+"]", out)
		}
	case reflect.Map:
		for _, c := range diffMap("", a, b) {
			p := path + q(c.Key)
			if c.Kind != Modified {
				*out = append(*out, p)
				continue
			}
			for _, sub := range c.Fields {
				if sub == "." {
					*out = append(*out, p)
				} else {
					*out = append(*out, p+joinPath(sub))
				}
			}
		}
	case reflect.Struct:
		for _, sf := range fieldsOf(a.Type()) {
			diffValue(a.Field(sf.index), b.Field(sf.index), sf, attrPath(path, sf.name), out)
		}
	default:
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			mark()
		}
	}
}

func joinPath(sub string) string {
	if sub == "" || sub[0] == '[' || sub[0] == '/' {
		return sub
	}
	return "." + sub
}

func diffJSON(a, b, path string, out *[]string) {
	if jsonEqual(a, b) {
		return
	}
	ta, errA := decodeJSONObject(a)
	tb, errB := decodeJSONObject(b)
	if errA != nil || errB != nil {
		*out = append(*out, path)
		return
	}
	jsonPaths(ta, tb, path, out)
}

func jsonPaths(a, b any, path string, out *[]string) {
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if !aok || !bok {
		if !reflect.DeepEqual(normalizeJSON(a), normalizeJSON(b)) {
			*out = append(*out, path)
		}
		return
	}
	keys := map[string]bool{}
	for k := range am {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		av, ina := am[k]
		bv, inb := bm[k]
		if ina != inb {
			*out = append(*out, path+"/"+k)
			continue
		}
		jsonPaths(av, bv, path+"/"+k, out)
	}
}

func normalizeJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
