package tfvars

import (
	"reflect"
	"strings"
	"sync"
)

type field struct {
	name     string
	index    int
	optional bool
	json     bool
	set      bool
}

var fieldCache sync.Map // reflect.Type -> []field

// fieldsOf returns the tagged fields of a schema struct in declaration order.
// It panics on a malformed tag because that is a programming error in this
// package, not an input error.
func fieldsOf(t reflect.Type) []field {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.([]field)
	}
	var out []field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag, ok := sf.Tag.Lookup("tf")
		if !ok || !sf.IsExported() {
			panic("tfvars: untagged schema field " + t.Name() + "." + sf.Name)
		}
		parts := strings.Split(tag, ",")
		f := field{name: parts[0], index: i}
		for _, p := range parts[1:] {
			switch p {
			case "optional":
				f.optional = true
			case "json":
				f.json = true
			case "set":
				f.set = true
			default:
				panic("tfvars: unknown tag option " + p)
			}
		}
		k := sf.Type.Kind()
		if f.optional && k != reflect.Pointer && k != reflect.Slice && k != reflect.Map {
			panic("tfvars: optional field must be a pointer, slice or map: " + t.Name() + "." + sf.Name)
		}
		out = append(out, f)
	}
	fieldCache.Store(t, out)
	return out
}
