package gitops

import (
	"bytes"
	"strings"
	"testing"
)

const testIntentID = "0123456789abcdef0123456789abcdef"

func testIntentJSON() string {
	return `{
  "version": 1,
  "id": "` + testIntentID + `",
  "requestedBy": {"pingoneUserId": "user-1", "username": "chase"},
  "source": {"id": "source", "region": "us"},
  "target": {"id": "target", "region": "us"},
  "selected": ["flow"],
  "mappings": {"flow": "dest"},
  "description": "Promote the login flow"
}`
}

func TestIntentDecodesAndBuildsRedactedRequestBoundToIntentID(t *testing.T) {
	in, err := DecodeIntent(strings.NewReader(testIntentJSON()))
	if err != nil {
		t.Fatal(err)
	}
	src, dst := testInventories("https://api.pingone.com")
	r, err := RequestFromIntent(in, src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != testIntentID {
		t.Fatalf("request ID %q not bound to intent ID", r.ID)
	}
	if !strings.Contains(r.Description, "Requested by chase (PingOne user user-1)") || !strings.Contains(r.Description, "Promote the login flow") {
		t.Fatalf("requester not recorded: %q", r.Description)
	}
	raw, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-value")) {
		t.Fatal("built request leaked a secret")
	}
	if _, err = Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("built request does not pass strict request validation: %v", err)
	}
}

// Regression for QE-J1 applied to the new intent format: a reviewer must see
// the value that takes effect.
func TestIntentRejectsDuplicateAndCaseVariantKeys(t *testing.T) {
	base := testIntentJSON()
	cases := map[string]string{
		"duplicate description": strings.Replace(base, `"description": "Promote the login flow"`, `"description": "Promote the login flow", "description": "something else"`, 1),
		"case-variant top key":  strings.Replace(base, `"description": "Promote the login flow"`, `"description": "Promote the login flow", "DESCRIPTION": "x"`, 1),
		"wrong-case key only":   strings.Replace(base, `"selected"`, `"Selected"`, 1),
		"duplicate nested key":  strings.Replace(base, `"source": {"id": "source", "region": "us"}`, `"source": {"id": "source", "region": "us", "id": "other"}`, 1),
		"duplicate mapping key": strings.Replace(base, `"mappings": {"flow": "dest"}`, `"mappings": {"flow": "dest", "flow": "other"}`, 1),
		"unknown field":         strings.Replace(base, `"version": 1,`, `"version": 1, "extra": true,`, 1),
		"two documents":         base + base,
	}
	for name, doc := range cases {
		if _, err := DecodeIntent(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Arrays of objects and nested values must not confuse the key walk.
	if err := checkJSONKeys([]byte(`{"a":[{"x":1},{"x":2}],"b":{"x":{"y":[1,{"z":2}]}},"c":"x"}`)); err != nil {
		t.Fatalf("valid nesting rejected: %v", err)
	}
}

func TestIntentValidationRejectsUnsafeValues(t *testing.T) {
	good := func() Intent {
		in, err := DecodeIntent(strings.NewReader(testIntentJSON()))
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	for name, mutate := range map[string]func(*Intent){
		"bad id":           func(in *Intent) { in.ID = "../" },
		"no requester":     func(in *Intent) { in.RequestedBy = Requester{} },
		"control in name":  func(in *Intent) { in.RequestedBy.Username = "a\x1bb" },
		"same env":         func(in *Intent) { in.Target = in.Source },
		"bad region":       func(in *Intent) { in.Target.Region = "moon" },
		"traversal env":    func(in *Intent) { in.Source.ID = "../x" },
		"no selection":     func(in *Intent) { in.Selected = nil },
		"dup selection":    func(in *Intent) { in.Selected = []string{"flow", "flow"} },
		"mapping conflict": func(in *Intent) { in.Mappings = map[string]string{"a": "z", "b": "z"} },
		"long description": func(in *Intent) { in.Description = strings.Repeat("x", 3001) },
	} {
		in := good()
		mutate(&in)
		if ValidateIntent(in) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	in := good()
	src, dst := testInventories("https://api.pingone.com")
	src.EnvironmentID = "elsewhere"
	if _, err := RequestFromIntent(in, src, dst); err == nil {
		t.Fatal("built a request from inventories of other environments")
	}
}
