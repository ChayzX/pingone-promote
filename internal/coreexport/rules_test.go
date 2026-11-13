package coreexport

import (
	"context"
	"strings"
	"testing"

	"pingone-promote/internal/catalog"
	"pingone-promote/internal/pingone"
)

func TestCondForms(t *testing.T) {
	raw := map[string]any{"name": "p1:read:user:extra", "default": false, "from": map[string]any{"emailAddress": "a@b.c"}}
	for c, want := range map[string]bool{
		"name=p1:read:user:extra":               true,
		"default=true||name=p1:read:user:extra": true,
		"default=true||name=Single_Factor":      false,
		"name~^p1:(read|update):user:.+$":       true,
		"name!~^p1:(read|update):user:.+$":      false,
		"from.emailAddress~.":                   true,
		"missing.field~.":                       false,
		"default!=true":                         true,
	} {
		if got := Cond(c, raw); got != want {
			t.Errorf("%s: %v, want %v", c, got, want)
		}
	}
	if Cond("name!~^p1:(read|update):user:.+$", map[string]any{"name": "p1:verify:user"}) != true {
		t.Error("a predefined PingOne API scope is not built in")
	}
}

func TestDisallowedWalksLists(t *testing.T) {
	raw := map[string]any{"quotas": []any{
		map[string]any{"deliveryMethods": []any{"SMS", "Voice"}},
		map[string]any{"deliveryMethods": []any{"SMS", "WhatsApp"}},
	}}
	if got := strings.Join(disallowed(raw, "quotas.deliveryMethods", []string{"SMS", "Voice"}), ","); got != "WhatsApp" {
		t.Fatalf("got %q", got)
	}
	if got := disallowed(map[string]any{}, "quotas.deliveryMethods", []string{"SMS"}); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}

// mapReader answers from fixed data, as the API answered a real tenant.
type mapReader struct {
	lists map[string][]map[string]any
	gets  map[string]map[string]any
}

func (m mapReader) List(_ context.Context, _, path, _ string) ([]map[string]any, error) {
	return m.lists[path], nil
}

func (m mapReader) Get(_ context.Context, _, path string) (map[string]any, error) {
	if o, ok := m.gets[path]; ok {
		return o, nil
	}
	return nil, &pingone.APIError{Status: 404, Path: path}
}

func def(t *testing.T, typ string) *catalog.Def {
	t.Helper()
	d, ok := catalog.Get(typ)
	if !ok {
		t.Fatalf("no catalog entry for %s", typ)
	}
	return d
}

// Shapes the real API returned that the provider's schema does not
// predict (a live tenant, 2026-11).
func TestRealAPIShapes(t *testing.T) {
	const env = "12e2ce5a-d22b-4ac4-802f-060df245995d"
	var warnings []string
	warn := func(s string) { warnings = append(warnings, s) }

	// The form list leaves out components; each form is read on its own.
	list := map[string]any{"id": "f1", "name": "Sign On", "category": "CUSTOM", "cols": 4}
	full := map[string]any{"id": "f1", "name": "Sign On", "category": "CUSTOM", "cols": 4,
		"components": map[string]any{"fields": []any{map[string]any{"type": "SUBMIT_BUTTON", "position": map[string]any{"row": 0, "col": 0}}}}}
	r := mapReader{lists: map[string][]map[string]any{"forms": {list}}, gets: map[string]map[string]any{"forms/f1": full}}
	forms, err := readType(context.Background(), r, env, def(t, "pingone_form"), nil, warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(forms) != 1 || forms[0].Raw["components"] == nil {
		t.Fatalf("the form was not read with its components: %+v", forms)
	}
	if miss := MissingRequired(def(t, "pingone_form"), list, nil); len(miss) == 0 {
		t.Error("a form without components is not reported as incomplete")
	}

	// Unconfigured settings read as {} and are not copied, silently.
	for _, typ := range []string{"pingone_forms_recaptcha_v2"} {
		d := def(t, typ)
		got, err := readType(context.Background(), mapReader{gets: map[string]map[string]any{d.Path: {}}}, env, d, nil, warn)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("%s: an unconfigured setting is copied", typ)
		}
	}
	if len(warnings) != 0 {
		t.Errorf("warnings for unconfigured settings: %v", warnings)
	}

	// The key rotation policy's subject is "dn".
	krp := map[string]any{"id": "k1", "name": "Policy", "algorithm": "RSA", "keyLength": 2048, "rotationPeriod": 90,
		"signatureAlgorithm": "SHA256withRSA", "usageType": "SIGNING", "validityPeriod": 365, "dn": "CN=Policy"}
	if miss := MissingRequired(def(t, "pingone_key_rotation_policy"), krp, nil); len(miss) != 0 {
		t.Errorf("key rotation policy: missing %v", miss)
	}

	// Administrators' MFA can only be enforced through the provider.
	d := def(t, "pingone_administrator_security")
	got, err := readType(context.Background(), mapReader{gets: map[string]map[string]any{d.Path: {"mfaStatus": "OPT_OUT", "authenticationMethod": "PINGONE"}}}, env, d, nil, warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "OPT_OUT") {
		t.Errorf("administrator security with MFA opt-out: %d objects, warnings %v", len(got), warnings)
	}

	// A role assignment names its scope as {id, type}.
	ra := def(t, "pingone_application_role_assignment")
	for typ, attr := range map[string]string{"ENVIRONMENT": "scope_environment_id", "ORGANIZATION": "scope_organization_id", "POPULATION": "scope_population_id"} {
		o := &Object{Type: ra.Type, ID: "ra1", Label: "x", Raw: map[string]any{"id": "ra1", "role": map[string]any{"id": "r1"},
			"scope": map[string]any{"id": "s1", "type": typ}}}
		rd := &renderer{in: RenderInput{Inventory: &Inventory{EnvironmentID: env}, IDs: map[string]string{}}, out: &Rendered{Unresolved: map[string]string{}, Referenced: map[string]string{}},
			vars: map[string]string{}, values: map[string]string{}, obj: o, def: ra}
		text, err := rd.block()
		if err != nil {
			t.Fatal(err)
		}
		n := strings.Count(text, "scope_")
		if !strings.Contains(text, attr+` = "s1"`) || n != 1 {
			t.Errorf("%s scope rendered as:\n%s", typ, text)
		}
	}
}

// Values the API does not hold on the object itself.
func TestDerivedRequiredValues(t *testing.T) {
	ra := def(t, "pingone_resource_attribute")
	attr := map[string]any{"id": "a1", "name": "middle_name", "type": "PREDEFINED", "value": "${user.name.middle}"}
	if miss := MissingRequired(ra, attr, map[string]any{"type": "OPENID_CONNECT"}); len(miss) != 0 {
		t.Errorf("OpenID attribute: missing %v", miss)
	}
	mp := def(t, "pingone_mfa_device_policy_default")
	on := map[string]any{"enabled": true}
	pol := map[string]any{"id": "p1", "name": "Default MFA Policy", "default": true, "sms": on, "email": on, "mobile": on, "totp": on, "voice": on}
	if miss := MissingRequired(mp, pol, nil); len(miss) != 0 {
		t.Errorf("PingOne MFA policy: missing %v", miss)
	}
	pol["desktop"] = map[string]any{"enabled": true}
	if miss := MissingRequired(mp, pol, nil); len(miss) != 1 || miss[0] != "policy_type" {
		t.Errorf("a PingID policy is not reported: missing %v", miss)
	}
	o := &Object{Type: ra.Type, ID: "a1", Label: "x", Raw: attr, Parent: &Object{Type: "pingone_resource", ID: "r1", Raw: map[string]any{"type": "OPENID_CONNECT"}}}
	rd := &renderer{in: RenderInput{Inventory: &Inventory{EnvironmentID: "e"}, IDs: map[string]string{}}, out: &Rendered{Unresolved: map[string]string{}, Referenced: map[string]string{}},
		vars: map[string]string{}, values: map[string]string{}, obj: o, def: ra}
	text, err := rd.block()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `resource_type = "OPENID_CONNECT"`) {
		t.Errorf("resource_type not rendered from the parent:\n%s", text)
	}
}
