package pingone

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInventoryVersionPinnedAndAllPages(t *testing.T) {
	var origin string
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			posts++
			t.Errorf("mutating pinned read: %s", r.Method)
		}
		switch r.URL.Path {
		case "/v1/environments/source":
			fmt.Fprint(w, `{"id":"source","organization":{"id":"org"}}`)
		case "/v1/environments/source/snapshots/source/versions/pin":
			fmt.Fprint(w, `{"snapshotId":"pin","resource":{"id":"source"},"status":"COMPLETE"}`)
		case "/v1/environments/source/snapshots/source/versions/pin/embeddedSnapshots":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprintf(w, `{"_embedded":{"referencedResources":[{"resource":{"id":"two"},"resourceUrl":%q,"configuration":{"name":"second"}}]}}`, origin+"/v1/environments/source/flows/two")
			} else {
				fmt.Fprintf(w, `{"count":2,"_embedded":{"referencedResources":[{"resource":{"id":"one"},"resourceUrl":%q,"configuration":{"name":"first"}}]},"_links":{"next":{"href":"?page=2"}}}`, origin+"/v1/environments/source/flows/one")
			}
		default:
			t.Errorf("unpinned path %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	origin = server.URL
	c := &Client{BaseURL: origin, HTTP: server.Client(), AccessToken: "test"}
	inv, err := c.InventoryVersion(context.Background(), "source", "pin")
	if err != nil || len(inv.Resources) != 2 || inv.SnapshotID != "pin" || posts != 0 {
		t.Fatalf("inventory=%+v err=%v posts=%d", inv, err, posts)
	}
}

func TestInventoryVersionRejectsIdentityAndIncomplete(t *testing.T) {
	for _, snapshot := range []string{`{"snapshotId":"other","resource":{"id":"source"},"status":"COMPLETE"}`, `{"snapshotId":"pin","resource":{"id":"source"},"status":"PREPARING"}`} {
		t.Run(snapshot, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/versions/") {
					fmt.Fprint(w, snapshot)
				} else {
					fmt.Fprint(w, `{"id":"source","organization":{"id":"org"}}`)
				}
			}))
			defer s.Close()
			c := &Client{BaseURL: s.URL, HTTP: s.Client(), AccessToken: "test"}
			if _, err := c.InventoryVersion(context.Background(), "source", "pin"); err == nil {
				t.Fatal("accepted invalid pinned snapshot")
			}
		})
	}
}

func TestInventoryVersionRejectsOddResourcePathIdentity(t *testing.T) {
	var s *httptest.Server
	s = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/environments/source":
			fmt.Fprint(w, `{"id":"source","organization":{"id":"org"}}`)
		case "/v1/environments/source/snapshots/source/versions/pin":
			fmt.Fprint(w, `{"snapshotId":"pin","resource":{"id":"source"},"status":"COMPLETE"}`)
		case "/v1/environments/source/snapshots/source/versions/pin/embeddedSnapshots":
			fmt.Fprintf(w, `{"_embedded":{"referencedResources":[{"resource":{"id":"versions"},"resourceUrl":%q,"configuration":{"name":"Malformed"}}]}}`, s.URL+"/v1/environments/source/flows/flow/versions")
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := &Client{BaseURL: s.URL, HTTP: s.Client(), AccessToken: "test"}
	if _, err := c.InventoryVersion(context.Background(), "source", "pin"); err == nil {
		t.Fatal("accepted an odd-length resource path whose last segment is a path type")
	}
}
