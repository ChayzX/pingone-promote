// Package strictjson rejects ambiguous encodings at reviewed file boundaries.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
)

var errEncoding = errors.New("JSON must use unique keys and exact schema field names")
var rawMessageType = reflect.TypeFor[json.RawMessage]()

// Unmarshal rejects duplicate object keys and case aliases of struct fields.
// Dynamic configuration maps retain their case-sensitive keys. Callers must
// bound input size before calling; errors never include input values.
func Unmarshal(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := value(d, reflect.TypeOf(out)); err != nil {
		return errEncoding
	}
	if _, err := d.Token(); err != io.EOF {
		return errEncoding
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errEncoding
	}
	return nil
}

func value(d *json.Decoder, typ reflect.Type) error {
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == rawMessageType {
		typ = nil
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		fields := map[string]reflect.Type{}
		isStruct := typ != nil && typ.Kind() == reflect.Struct
		if isStruct {
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if !f.IsExported() {
					continue
				}
				name := strings.Split(f.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = f.Name
				}
				fields[name] = f.Type
			}
		}
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errEncoding
			}
			seen[key] = true
			var child reflect.Type
			if isStruct {
				child, ok = fields[key]
				if !ok {
					return errEncoding
				}
			} else if typ != nil && typ.Kind() == reflect.Map {
				child = typ.Elem()
			}
			if err := value(d, child); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return errEncoding
		}
	case '[':
		var child reflect.Type
		if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
			child = typ.Elem()
		}
		for d.More() {
			if err := value(d, child); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return errEncoding
		}
	default:
		return errEncoding
	}
	return nil
}
