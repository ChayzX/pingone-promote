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

func TestInventorySupportsNativeSnapshotIdentities(t *testing.T) {
	cases := []struct{ name, path, id, kind string }{
		{"flow-version", "flows/flow-one/versions/3/details", "flowId:flow-one-version:3", "flows / versions / details"},
		{"branding", "brandingSettings", "type:brandingsettings", "brandingSettings"},
		{"mfa", "mfaSettings", "type:mfasettings", "mfaSettings"},
		{"notifications", "notificationsSettings", "type:notificationssettings", "notificationsSettings"},
		{"email-delivery", "notificationsSettings/emailDeliverySettings", "type:notificationssettings:emaildeliverysettings", "notificationsSettings / emailDeliverySettings"},
		{"sso-cookie", "ssoCookieSettings", "type:ssocookiesettings", "ssoCookieSettings"},
		{"expanded-attribute", "applications/app-one/attributes/attribute-one?expand=oidcScopes", "attribute-one", "applications / attributes"},
		{"expanded-grant", "applications/app-one/grants/grant-one?expand=scopes", "grant-one", "applications / grants"},
	}
	for _, tc := range cases {
		for _, pinned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pinned=%t", tc.name, pinned), func(t *testing.T) {
				inv, err := readIdentityFixture(t, tc.path, tc.id, pinned)
				if err != nil {
					t.Fatal(err)
				}
				if len(inv.Resources) != 1 || inv.Resources[0].ID != tc.id || inv.Resources[0].Type != tc.kind || inv.Resources[0].Configuration["name"] != "Example" {
					t.Fatalf("native identity was changed or omitted: %+v", inv.Resources)
				}
				if strings.Contains(inv.Resources[0].URL, "?") {
					t.Fatal("inventory retained a read-expansion query in a promotion resource URL")
				}
			})
		}
	}
}

func TestInventoryRejectsUnboundNativeSnapshotIdentities(t *testing.T) {
	cases := []struct{ name, path, id string }{
		{"wrong-flow", "flows/flow-one/versions/3/details", "flowId:flow-two-version:3"},
		{"wrong-version", "flows/flow-one/versions/3/details", "flowId:flow-one-version:4"},
		{"wrong-singleton", "brandingSettings", "type:mfasettings"},
		{"unknown-singleton", "arbitrarySettings", "type:arbitrarysettings"},
		{"wrong-child", "notificationsSettings/emailDeliverySettings", "type:notificationssettings"},
		{"extra-suffix", "flows/flow-one/versions/3/details/extra", "flowId:flow-one-version:3"},
		{"leading-zero-version", "flows/flow-one/versions/03/details", "flowId:flow-one-version:03"},
		{"missing-version", "flows/flow-one/versions/details", "flowId:flow-one-version:3"},
		{"collection-only", "flows/flow-one/versions", "versions"},
		{"foreign-environment", "../other/brandingSettings", "type:brandingsettings"},
		{"query", "brandingSettings?other=true", "type:brandingsettings"},
		{"wrong-expansion", "applications/app-one/grants/grant-one?expand=oidcScopes", "grant-one"},
		{"wrong-expansion-path", "flows/flow-one?expand=scopes", "flow-one"},
		{"extra-query", "applications/app-one/grants/grant-one?expand=scopes&other=true", "grant-one"},
		{"duplicate-query", "applications/app-one/grants/grant-one?expand=scopes&expand=scopes", "grant-one"},
	}
	for _, tc := range cases {
		for _, pinned := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pinned=%t", tc.name, pinned), func(t *testing.T) {
				if _, err := readIdentityFixture(t, tc.path, tc.id, pinned); err == nil {
					t.Fatal("accepted a snapshot identity that does not bind to its resource URL")
				}
			})
		}
	}
}

func readIdentityFixture(t *testing.T, path, id string, pinned bool) (Inventory, error) {
	t.Helper()
	var origin string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/environments/source":
			fmt.Fprint(w, `{"id":"source","organization":{"id":"org"}}`)
		case !pinned && r.Method == "POST" && r.URL.Path == "/v1/environments/source/snapshots":
			fmt.Fprint(w, `{"resource":{"id":"source"},"snapshotId":"pin","status":"COMPLETE"}`)
		case pinned && r.Method == "GET" && r.URL.Path == "/v1/environments/source/snapshots/source/versions/pin":
			fmt.Fprint(w, `{"resource":{"id":"source"},"snapshotId":"pin","status":"COMPLETE"}`)
		case r.Method == "GET" && r.URL.Path == "/v1/environments/source/snapshots/source/versions/pin/embeddedSnapshots":
			json.NewEncoder(w).Encode(map[string]any{"_embedded": map[string]any{"referencedResources": []any{map[string]any{"id": "snapshot-record", "resource": map[string]string{"id": id}, "resourceUrl": origin + "/v1/environments/source/" + path, "status": "COMPLETE", "configuration": map[string]any{"name": "Example"}}}}, "count": 1})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	origin = server.URL
	client := &Client{BaseURL: origin, HTTP: server.Client(), AccessToken: "test"}
	if pinned {
		return client.InventoryVersion(context.Background(), "source", "pin")
	}
	return client.Inventory(context.Background(), "source")
}
