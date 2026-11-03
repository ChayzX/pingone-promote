package davinci_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pingone-promote/internal/davinci"
	"pingone-promote/internal/davinci/davincitest"
	"pingone-promote/internal/pingone"
)

const fixtureEnv = "00000000-0000-4000-8000-0000000000a1"

var fixtureDir = filepath.Join("testdata", "source")

func readFixture(t *testing.T, statuses map[string]int) (*davinci.Inventory, *davincitest.Server) {
	t.Helper()
	srv, client := davincitest.New(t, fixtureDir, fixtureEnv)
	for k, v := range statuses {
		srv.SetStatus(k, v)
	}
	return davinci.Read(context.Background(), client, fixtureEnv), srv
}

func TestReadFullInventory(t *testing.T) {
	inv, srv := readFixture(t, nil)
	if len(inv.Partial) != 0 {
		t.Fatalf("partial: %+v", inv.Partial)
	}
	if len(inv.Flows) != 3 || len(inv.Connections) != 4 || len(inv.Variables) != 6 || len(inv.Applications) != 1 || len(inv.FlowPolicies) != 1 {
		t.Fatalf("counts: flows=%d conns=%d vars=%d apps=%d pols=%d", len(inv.Flows), len(inv.Connections), len(inv.Variables), len(inv.Applications), len(inv.FlowPolicies))
	}
	// Flows are fetched by ID for their graph after the list.
	for _, f := range inv.Flows {
		if f.GraphData == nil || len(f.GraphData.Elements.Nodes) == 0 {
			t.Fatalf("flow %s has no graph", f.ID)
		}
	}
	if inv.FlowPolicies[0].ApplicationID != inv.Applications[0].ID {
		t.Fatal("policy not bound to its application")
	}
	// Variables span two pages.
	var paged bool
	for _, r := range srv.Requests {
		if strings.Contains(r, "cursor=p2") {
			paged = true
		}
		if !strings.HasPrefix(r, "GET ") {
			t.Fatalf("non-GET request %q", r)
		}
	}
	if !paged {
		t.Fatal("second variables page was not requested")
	}
}

func TestReadNeverDecodesApplicationSecrets(t *testing.T) {
	inv, _ := readFixture(t, nil)
	out, _ := json.Marshal(inv.Applications)
	for _, canary := range []string{"LEAKCANARY-appkey-0005", "LEAKCANARY-appsecret-0006"} {
		if strings.Contains(string(out), canary) {
			t.Fatalf("application secret %s was decoded", canary)
		}
	}
}

func TestMaskedValues(t *testing.T) {
	inv, _ := readFixture(t, nil)
	var sso davinci.ConnectorInstance
	for _, c := range inv.Connections {
		if c.Name == "PingOne" {
			sso = c
		}
	}
	if got := davinci.MaskedPaths(sso.Properties); !reflect.DeepEqual(got, []string{"/clientSecret/value"}) {
		t.Fatalf("masked paths %v", got)
	}
}

func TestPartialInventoryIsClassifiedPerKind(t *testing.T) {
	cases := []struct {
		name     string
		statuses map[string]int
		kind     davinci.Kind
		reason   davinci.Reason
		status   int
		count    int
	}{
		{"variables 403", map[string]int{"/variables": 403}, davinci.KindVariables, davinci.ReasonForbidden, 403, 0},
		{"connections 401", map[string]int{"/connectorInstances": 401}, davinci.KindConnections, davinci.ReasonUnauthorized, 401, 0},
		{"one flow 403", map[string]int{"/flows/f0000000000000000000000000000002": 403}, davinci.KindFlows, davinci.ReasonForbidden, 403, 1},
		{"policies 500", map[string]int{"/flowPolicies": 500}, davinci.KindFlowPolicies, davinci.ReasonServer, 500, 1},
		{"flows 404", map[string]int{"/flows": 404}, davinci.KindFlows, davinci.ReasonNotFound, 404, 0},
		{"apps 429", map[string]int{"/davinciApplications": 429}, davinci.KindApplications, davinci.ReasonRateLimited, 429, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, _ := readFixture(t, tc.statuses)
			found := false
			for _, f := range inv.Partial {
				if f.Kind == tc.kind {
					found = true
					if f.Reason != tc.reason || f.Status != tc.status || f.Count != tc.count {
						t.Fatalf("failure %+v", f)
					}
				}
			}
			if !found || inv.Complete(tc.kind) {
				t.Fatalf("partial %+v", inv.Partial)
			}
			// Other kinds are still read.
			if tc.kind != davinci.KindConnections && len(inv.Connections) != 4 {
				t.Fatal("unrelated kind was not read")
			}
		})
	}
	// Application list failure makes policies incomplete too.
	inv, _ := readFixture(t, map[string]int{"/davinciApplications": 403})
	if inv.Complete(davinci.KindFlowPolicies) {
		t.Fatal("policies reported complete without applications")
	}
}

func TestUnauthorizedTokenIsClassified(t *testing.T) {
	srv, _ := davincitest.New(t, fixtureDir, fixtureEnv)
	bad := &pingone.Client{BaseURL: srv.URL, AccessToken: "wrong", HTTP: srv.Client()}
	inv := davinci.Read(context.Background(), bad, fixtureEnv)
	// flows, connections, variables and applications fail; policies are
	// incomplete because no application could be listed.
	if len(inv.Partial) != 5 {
		t.Fatalf("partial %+v", inv.Partial)
	}
	for _, f := range inv.Partial {
		if f.Kind != davinci.KindFlowPolicies && f.Reason != davinci.ReasonUnauthorized {
			t.Fatalf("failure %+v", f)
		}
	}
}

func TestMalformedAndInvalidIDs(t *testing.T) {
	dir := t.TempDir()
	entries, _ := os.ReadDir(fixtureDir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(fixtureDir, e.Name()))
		os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600)
	}
	os.WriteFile(filepath.Join(dir, "connectorInstances.json"), []byte(`{"_embedded":{"connectorInstances":[{"id":"../../x","name":"bad"}]}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "davinciApplications.json"), []byte(`{"_embedded":{`), 0o600)
	srv, client := davincitest.New(t, dir, fixtureEnv)
	_ = srv
	inv := davinci.Read(context.Background(), client, fixtureEnv)
	got := map[davinci.Kind]davinci.Reason{}
	for _, f := range inv.Partial {
		got[f.Kind] = f.Reason
	}
	if got[davinci.KindConnections] != davinci.ReasonMalformed || got[davinci.KindApplications] != davinci.ReasonMalformed {
		t.Fatalf("partial %+v", inv.Partial)
	}
	if bad := davinci.Read(context.Background(), client, "../escape"); len(bad.Partial) != 5 {
		t.Fatal("invalid environment ID was not rejected")
	}
}

func TestClassify(t *testing.T) {
	if r, _ := davinci.Classify(errors.New("PingOne request did not return a response")); r != davinci.ReasonTransport {
		t.Fatal(r)
	}
	if r, _ := davinci.Classify(errors.New("incomplete PingOne pagination: received 1 of 2 resources")); r != davinci.ReasonIncomplete {
		t.Fatal(r)
	}
}
