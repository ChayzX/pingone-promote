package promote

import (
	"testing"

	"github.com/zclconf/go-cty/cty"

	"pingone-promote/internal/neutralize"
)

// A value given as JSON is written as text where the variable is a string
// (a DaVinci variable holding an object), and as given otherwise.
func TestValuesKeepTheDeclaredType(t *testing.T) {
	obj := cty.ObjectVal(map[string]cty.Value{"CSRauth": cty.True, "region": cty.StringVal("us")})
	got := asDeclared(&neutralize.Variable{Type: "string"}, obj)
	if got.Type() != cty.String || got.AsString() != `{"CSRauth":true,"region":"us"}` {
		t.Fatalf("string variable: %#v", got)
	}
	list := cty.TupleVal([]cty.Value{cty.StringVal("https://a.example.com")})
	if got := asDeclared(&neutralize.Variable{Type: "list(string)"}, list); !got.RawEquals(list) {
		t.Fatalf("list variable changed: %#v", got)
	}
	if got := asDeclared(nil, obj); !got.RawEquals(obj) {
		t.Fatalf("undeclared variable changed: %#v", got)
	}
}
