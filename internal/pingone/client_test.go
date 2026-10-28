package pingone

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPagesInspectsEveryPageAndRejectsExternalNext(t *testing.T) {
	for _, hostile := range []bool{false, true} {
		t.Run(map[bool]string{false: "all pages", true: "external link"}[hostile], func(t *testing.T) {
			var ts *httptest.Server
			ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("cursor") == "2" {
					w.Write([]byte(`{"_embedded":{"promotionSteps":[{"action":"PUT","sourceResource":{"id":"template"}}]},"count":2}`))
					return
				}
				next := ts.URL + r.URL.Path + "?cursor=2"
				if hostile {
					next = "https://attacker.example/steps"
				}
				json.NewEncoder(w).Encode(map[string]any{"_embedded": map[string]any{"promotionSteps": []any{map[string]any{"action": "PUT", "sourceResource": map[string]any{"id": "flow"}}}}, "_links": map[string]any{"next": map[string]any{"href": next}}, "count": 2})
			}))
			defer ts.Close()
			c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "token"}
			got, err := c.Pages(context.Background(), "/v1/environments/src/promotions/p/promotionSteps", "promotionSteps")
			if hostile {
				if err == nil {
					t.Fatal("external next was accepted")
				}
				return
			}
			if err != nil || len(got) != 2 || !strings.Contains(string(got[1]), "template") {
				t.Fatalf("lost later-page resource: %s %v", got, err)
			}
		})
	}
}
func TestPagesFailsOnIncompleteOrCyclicInventory(t *testing.T) {
	for _, body := range []string{`{"_embedded":{"referencedResources":[]},"count":2}`, `{"_embedded":{"unexpected":[]}}`, `{"_embedded":{"referencedResources":[]},"_links":{"next":{"href":"/v1/environments/src/items"}}}`} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, err := (&Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "t"}).Pages(context.Background(), "/v1/environments/src/items", "referencedResources")
		ts.Close()
		if err == nil {
			t.Fatalf("accepted incomplete response: %s", body)
		}
	}
}
func TestOAuthUsesWorkerAuthEnvironmentAndDoesNotForwardRedirect(t *testing.T) {
	var tokenCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker-env/as/token" {
			tokenCalls++
			r.ParseForm()
			u, p, ok := r.BasicAuth()
			if !ok || u != "client" || p != "secret" || r.Form.Get("grant_type") != "client_credentials" {
				t.Error("bad OAuth request")
			}
			w.Write([]byte(`{"access_token":"access","expires_in":300}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("missing API bearer")
		}
		w.Write([]byte(`{"id":"managed-env"}`))
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, TokenURL: ts.URL + "/worker-env/as/token", HTTP: ts.Client(), Profile: Profile{ClientID: "client"}, Secret: "secret"}
	for range 2 {
		var out map[string]any
		if err := c.Do(context.Background(), "GET", "/v1/environments/managed-env", "", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("token not cached: %d", tokenCalls)
	}
}
func TestInventoryUsesOriginalIDsAndPinnedVersion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/environments/src":
			w.Write([]byte(`{"id":"src","organization":{"id":"tenant"}}`))
		case r.Method == "POST" && r.URL.Path == "/v1/environments/src/snapshots":
			var b map[string]string
			json.NewDecoder(r.Body).Decode(&b)
			if !strings.HasSuffix(b["baseResourceURL"], "/environments/src") || r.URL.Query().Get("expand") != "all" {
				t.Error("bad root snapshot request")
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"resource":{"id":"src"},"snapshotId":"version1","status":"WAITING"}`))
		case r.URL.Path == "/v1/environments/src/snapshots/src/versions/version1":
			w.Write([]byte(`{"resource":{"id":"src"},"snapshotId":"version1","status":"COMPLETE"}`))
		case strings.HasSuffix(r.URL.Path, "/versions/version1/embeddedSnapshots"):
			w.Write([]byte(`{"_embedded":{"referencedResources":[{"id":"record-not-resource","snapshotId":"child-version","resource":{"id":"flow1"},"resourceUrl":"` + "http://" + r.Host + `/v1/environments/src/flows/flow1","configuration":{"id":"flow1","name":"Customer sign in"},"referencedResources":["connection1"],"status":"COMPLETE"}]},"count":1}`))
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "t"}
	inv, err := c.Inventory(context.Background(), "src")
	if err != nil {
		t.Fatal(err)
	}
	if inv.SnapshotID != "version1" || inv.OrganizationID != "tenant" || len(inv.Resources) != 1 || inv.Resources[0].ID != "flow1" || inv.Resources[0].Name != "Customer sign in" || inv.Resources[0].Type != "flows" {
		t.Fatalf("wrong inventory: %+v", inv)
	}
}

func TestInventoryRejectsResourceURLWhoseFinalIDDiffers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/environments/src":
			fmt.Fprint(w, `{"id":"src","organization":{"id":"tenant"}}`)
		case r.Method == "POST" && r.URL.Path == "/v1/environments/src/snapshots":
			fmt.Fprint(w, `{"resource":{"id":"src"},"snapshotId":"version1","status":"COMPLETE"}`)
		case strings.HasSuffix(r.URL.Path, "/versions/version1/embeddedSnapshots"):
			fmt.Fprintf(w, `{"_embedded":{"referencedResources":[{"resource":{"id":"approved-flow"},"resourceUrl":%q,"configuration":{"name":"Customer sign in"}}]}}`, "http://"+r.Host+"/v1/environments/src/flows/different-flow")
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "t"}
	if _, err := c.Inventory(context.Background(), "src"); err == nil {
		t.Fatal("inventory accepted a resource URL that names a different resource ID")
	}
}

func TestInventoryRejectsChangedIdentityWhileSnapshotIsPreparing(t *testing.T) {
	gets := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/environments/src":
			fmt.Fprint(w, `{"id":"src","organization":{"id":"tenant"}}`)
		case r.Method == "POST" && r.URL.Path == "/v1/environments/src/snapshots":
			fmt.Fprint(w, `{"resource":{"id":"src"},"snapshotId":"version1","status":"WAITING"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/environments/src/snapshots/src/versions/version1":
			gets++
			fmt.Fprint(w, `{"resource":{"id":"other-env"},"snapshotId":"version2","status":"COMPLETE"}`)
		case strings.HasSuffix(r.URL.Path, "/versions/version1/embeddedSnapshots"):
			fmt.Fprint(w, `{"_embedded":{"referencedResources":[]}}`)
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "t"}
	if _, err := c.Inventory(context.Background(), "src"); err == nil || gets != 1 {
		t.Fatalf("accepted changed asynchronous snapshot identity: err=%v GETs=%d", err, gets)
	}
}

func TestInventoryRejectsPollMissingPinnedIdentity(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/environments/src":
			fmt.Fprint(w, `{"id":"src","organization":{"id":"tenant"}}`)
		case r.Method == "POST" && r.URL.Path == "/v1/environments/src/snapshots":
			fmt.Fprint(w, `{"resource":{"id":"src"},"snapshotId":"version1","status":"WAITING"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/environments/src/snapshots/src/versions/version1":
			fmt.Fprint(w, `{"status":"COMPLETE"}`)
		case strings.HasSuffix(r.URL.Path, "/versions/version1/embeddedSnapshots"):
			fmt.Fprint(w, `{"_embedded":{"referencedResources":[]}}`)
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "t"}
	if _, err := c.Inventory(context.Background(), "src"); err == nil {
		t.Fatal("accepted a completed poll that omitted its environment and snapshot identities")
	}
}

// Live acceptance finding: the PingOne API gateway answers unknown routes with a
// message that reflects a SHA-256 hash of the Authorization header. Runner errors
// are printed to CI job logs, so credential-derived material must not be relayed.
func TestGatewayAuthorizationReflectionIsNotRelayed(t *testing.T) {
	const hash = "rz05vny4b9Gv0kuFUimjEYoQ0123456789abcdefXYZ="
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"message":"Invalid key=value pair (missing equal-sign) in Authorization header (hashed with SHA-256 and encoded with Base64): '` + hash + `'."}`))
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "token"}
	err := c.Do(context.Background(), "GET", "/v1/environments/env/unknownRoute", "", nil, nil)
	if err == nil {
		t.Fatal("gateway rejection was accepted")
	}
	msg := err.Error()
	if strings.Contains(msg, hash) || strings.Contains(msg, "rz05") || strings.Contains(strings.ToLower(msg), "authorization header") {
		t.Fatalf("credential-derived gateway text relayed: %q", msg)
	}
	if !strings.Contains(msg, "403") {
		t.Fatalf("status lost: %q", msg)
	}
}

func TestErrorCodeDoesNotRelayAccessToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":"access-token-value","message":"request rejected"}`)
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "access-token-value"}
	err := c.Do(context.Background(), "GET", "/v1/environments/env", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("status diagnostic lost: %v", err)
	}
	if strings.Contains(err.Error(), "access-token-value") {
		t.Fatalf("server error code relayed the access token: %v", err)
	}
}

func TestErrorCodeRemainsReadableWithoutConfiguredSecret(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":"ACCESS_FAILED","message":"request rejected"}`)
	}))
	defer ts.Close()
	c := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "access"}
	err := c.Do(context.Background(), "GET", "/v1/environments/env", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "ACCESS_FAILED") {
		t.Fatalf("safe API error code was corrupted: %v", err)
	}
}

// PingOne distinguishes entitlement denial from role denial in details[].code.
// Dropping those codes makes snapshot discovery failures impossible to diagnose
// even when the worker has the documented roles.
func TestInventoryRetainsSafeAccessFailureDetailCodes(t *testing.T) {
	for _, detail := range []string{"LICENSE_EXCEEDED", "INSUFFICIENT_PERMISSIONS"} {
		t.Run(detail, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" && r.URL.Path == "/v1/environments/env" {
					fmt.Fprint(w, `{"id":"env","organization":{"id":"org"}}`)
					return
				}
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `{"code":"ACCESS_FAILED","message":"The request could not be completed.","details":[{"code":%q,"message":"secret-value token-value private-environment"},{"code":"token-value"},{"code":%q}]}`, detail, detail)
			}))
			defer ts.Close()
			client := &Client{BaseURL: ts.URL, HTTP: ts.Client(), AccessToken: "token-value", Secret: "secret-value"}
			_, err := client.Inventory(context.Background(), "env")
			if err == nil || !strings.Contains(err.Error(), detail) || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "inventory snapshot") {
				t.Fatalf("snapshot denial lost its safe diagnostic code: %v", err)
			}
			if strings.Count(err.Error(), detail) != 1 {
				t.Fatalf("duplicate detail codes were not bounded: %v", err)
			}
			for _, sensitive := range []string{"secret-value", "token-value", "private-environment"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("detail message exposed sensitive response content: %v", err)
				}
			}
		})
	}
}
