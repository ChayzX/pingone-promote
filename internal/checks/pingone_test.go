package checks

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pingone-promote/internal/pingone"
)

type tenant struct {
	optIn        string // "on", "off", "absent", "error"
	promotions   string // "ok", "notEnabled", "denied"
	envType      string
	wrongID      bool
	featuresWrap bool
}

func (tn tenant) server(t *testing.T) *pingone.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		env := parts[2]
		switch {
		case len(parts) == 3:
			id := env
			if tn.wrongID {
				id = "other"
			}
			typ := tn.envType
			if typ == "" {
				typ = "SANDBOX"
			}
			fmt.Fprintf(w, `{"id":%q,"name":"Env","type":%q,"organization":{"id":"org"}}`, id, typ)
		case parts[3] == "earlyAccessFeatures":
			switch tn.optIn {
			case "error":
				w.WriteHeader(403)
				w.Write([]byte(`{"code":"ACCESS_FAILED","message":"denied"}`))
			case "absent":
				w.Write([]byte(`{"features":[{"name":"Other","value":true}]}`))
			default:
				body := fmt.Sprintf(`[{"name":"Configuration Management","value":%v}]`, tn.optIn == "on")
				if tn.featuresWrap {
					fmt.Fprintf(w, `{"_embedded":{"features":%s}}`, body)
				} else {
					fmt.Fprintf(w, `{"features":%s}`, body)
				}
			}
		case parts[3] == "promotions":
			switch tn.promotions {
			case "notEnabled":
				w.WriteHeader(403)
				w.Write([]byte(`{"code":"ACCESS_FAILED","message":"This capability is not enabled for this environment."}`))
			case "denied":
				w.WriteHeader(403)
				w.Write([]byte(`{"code":"ACCESS_FAILED","message":"The request could not be completed. You do not have access to this resource."}`))
			default:
				w.Write([]byte(`{"_embedded":{"promotions":[]}}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	return &pingone.Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "eyJ-test-access-token-0123456789"}
}

func TestPingOnePreflightReadyTenant(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		c := tenant{optIn: "on", promotions: "ok", featuresWrap: wrap}.server(t)
		r := PingOnePreflight(context.Background(), c, EnvironmentLabels("src", "tgt"))
		if r.Failed() {
			t.Fatalf("ready tenant failed:\n%s", r.Markdown())
		}
		if p, _, _ := r.Counts(); p != 6 {
			t.Fatalf("expected 6 passes (3 per environment):\n%s", r.Markdown())
		}
		if !strings.HasPrefix(r.Checks[0].Name, "source ") {
			t.Fatalf("source not reported first:\n%s", r.Markdown())
		}
	}
}

// The trial tenant returned "not enabled" for an opted-in environment whose
// license lacked the entitlement; that must be told apart from a missing role.
func TestPingOnePreflightDistinguishesFeatureFromRole(t *testing.T) {
	cases := map[string]struct {
		tn   tenant
		name string
		want string
	}{
		"feature not active": {tenant{optIn: "on", promotions: "notEnabled"}, "target promotions API", "license"},
		"worker denied":      {tenant{optIn: "on", promotions: "denied"}, "target promotions API", "Environment Admin"},
		"opt-in off":         {tenant{optIn: "off", promotions: "ok"}, "target opt-in", "Manage Opt-Ins"},
		"wrong identity":     {tenant{optIn: "on", promotions: "ok", wrongID: true}, "target environment", "differs"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := PingOnePreflight(context.Background(), c.tn.server(t), EnvironmentLabels("", "tgt"))
			var got *Check
			for i := range r.Checks {
				if r.Checks[i].Name == c.name {
					got = &r.Checks[i]
				}
			}
			if got == nil || got.Status != Fail || !strings.Contains(got.Detail, c.want) {
				t.Fatalf("%s not failed with %q:\n%s", c.name, c.want, r.Markdown())
			}
		})
	}
}

func TestPingOnePreflightWarnings(t *testing.T) {
	r := PingOnePreflight(context.Background(), tenant{optIn: "error", promotions: "ok", envType: "PRODUCTION"}.server(t), EnvironmentLabels("", "tgt"))
	if r.Failed() {
		t.Fatalf("warnings escalated to failure:\n%s", r.Markdown())
	}
	if _, w, _ := r.Counts(); w != 2 {
		t.Fatalf("expected opt-in and production-type warnings:\n%s", r.Markdown())
	}
	r = PingOnePreflight(context.Background(), tenant{optIn: "absent", promotions: "ok"}.server(t), EnvironmentLabels("", "tgt"))
	if r.Failed() {
		t.Fatalf("absent feature listing failed:\n%s", r.Markdown())
	}
}

func TestEnvironmentLabels(t *testing.T) {
	if m := EnvironmentLabels("a", "a"); len(m) != 1 || m["target"] != "a" {
		t.Fatalf("duplicate not collapsed: %v", m)
	}
	if m := EnvironmentLabels("", ""); len(m) != 0 {
		t.Fatalf("empty ids kept: %v", m)
	}
}
