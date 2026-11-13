package coreexport

import (
	"strings"
	"testing"
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
