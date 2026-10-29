// Inventory cross-check (scratch copy only): serves fixture snapshot responses
// to the real Go client and prints the resulting inventory or error.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"

	"pingone-promote/internal/pingone"
)

type fx struct {
	Name      string            `json:"name"`
	Env       string            `json:"env"`
	Pin       string            `json:"pin"`
	EnvRecord json.RawMessage   `json:"envRecord"`
	Snapshot  json.RawMessage   `json:"snapshot"`
	Pages     []json.RawMessage `json:"pages"`
}

type out struct {
	Name      string             `json:"name"`
	Error     bool               `json:"error"`
	Message   string             `json:"message"`
	Org       string             `json:"org"`
	Snapshot  string             `json:"snapshot"`
	Resources []pingone.Resource `json:"resources"`
}

func main() {
	var all []fx
	if err := json.NewDecoder(os.Stdin).Decode(&all); err != nil {
		panic(err)
	}
	results := []out{}
	for _, f := range all {
		var origin string
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fix := func(raw json.RawMessage) string { return strings.ReplaceAll(string(raw), "ORIGIN", origin) }
			base := "/v1/environments/" + f.Env
			switch {
			case r.URL.Path == base:
				fmt.Fprint(w, fix(f.EnvRecord))
			case r.Method == "POST" && r.URL.Path == base+"/snapshots":
				fmt.Fprint(w, fix(f.Snapshot))
			case strings.HasSuffix(r.URL.Path, "/embeddedSnapshots"):
				page := 0
				fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
				if page < len(f.Pages) {
					fmt.Fprint(w, fix(f.Pages[page]))
				} else {
					http.NotFound(w, r)
				}
			case strings.Contains(r.URL.Path, "/snapshots/"):
				fmt.Fprint(w, fix(f.Snapshot))
			default:
				http.NotFound(w, r)
			}
		}))
		origin = srv.URL
		c := &pingone.Client{BaseURL: origin, HTTP: srv.Client(), AccessToken: "test"}
		inv, err := pingone.RunInventory(c, f.Env, f.Pin)
		o := out{Name: f.Name}
		if err != nil {
			o.Error, o.Message = true, err.Error()
		} else {
			o.Org, o.Snapshot = inv.OrganizationID, inv.SnapshotID
			for _, r := range inv.Resources {
				r.URL = strings.TrimPrefix(r.URL, origin)
				o.Resources = append(o.Resources, r)
			}
		}
		srv.Close()
		results = append(results, o)
	}
	_ = json.NewEncoder(os.Stdout).Encode(results)
}
