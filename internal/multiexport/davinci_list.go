package multiexport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"pingone-promote/internal/coreexport"
	"pingone-promote/internal/exporter"
	"pingone-promote/internal/neutralize"
	"pingone-promote/internal/secretscan"
)

// davinciList reads the DaVinci items straight from the API (flows,
// variables, connector instances, applications and their flow policies:
// a handful of requests, seconds), for the item list. The terraformer
// export, which the configuration itself comes from, takes a minute or
// more and runs meanwhile. Labels follow terraformer's; selections are
// matched to its export by ID, so a label that differs cannot misdirect
// one.
//
// It also returns a fingerprint of what changes when DaVinci changes
// (IDs, update times, flow versions and enabled state), never of values.
//
// values are the string variables' values by ID (secret ones are masked by
// the API and left out).
func davinciList(ctx context.Context, r coreexport.Reader, env string) (_ []exporter.Resource, fingerprint string, values map[string]string, _ error) {
	l, err := davinciListing(ctx, r, env)
	return l.items, l.fp, l.values, err
}

// dvListing is the quick DaVinci list: items, fingerprint, string
// variable values by ID and the API objects by ID.
type dvListing struct {
	items  []exporter.Resource
	fp     string
	values map[string]string
	raw    map[string]map[string]any
}

func davinciListing(ctx context.Context, r coreexport.Reader, env string) (dvListing, error) {
	var l dvListing
	type coll struct{ path, embedded string }
	colls := []coll{{"flows", "flows"}, {"variables", "variables"}, {"connectorInstances", "connectorInstances"}, {"davinciApplications", "davinciApplications"}}
	results := make([][]map[string]any, len(colls))
	errs := make([]error, len(colls))
	var wg sync.WaitGroup
	for i, c := range colls {
		wg.Add(1)
		go func(i int, c coll) {
			defer wg.Done()
			results[i], errs[i] = r.List(ctx, env, c.path, c.embedded)
		}(i, c)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return l, err
		}
	}
	flows, vars, conns, apps := results[0], results[1], results[2], results[3]
	policies := make([][]map[string]any, len(apps))
	perrs := make([]error, len(apps))
	for i, a := range apps {
		id, _ := a["id"].(string)
		if id == "" {
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			policies[i], perrs[i] = r.List(ctx, env, "davinciApplications/"+id+"/flowPolicies", "flowPolicies")
		}(i, id)
	}
	wg.Wait()
	for _, err := range perrs {
		if err != nil {
			return l, err
		}
	}

	var out []exporter.Resource
	var fp []string
	l.raw = map[string]map[string]any{}
	add := func(typ, label, name, id string, raw map[string]any, extra ...string) {
		out = append(out, exporter.Resource{Address: exporter.Address{Type: typ, Label: label}, Name: name, ID: id})
		l.raw[id] = raw
		line := typ + " " + id + " " + str(raw["updatedAt"])
		for _, k := range extra {
			line += " " + str(raw[k])
		}
		fp = append(fp, line)
	}
	for _, f := range flows {
		id, name := str(f["id"]), str(f["name"])
		label := neutralize.Label(name)
		add(neutralize.TypeFlow, label, name, id, f, "currentVersion", "publishedVersion", "deployedAt", "enabled")
		for _, c := range exporter.Companions(neutralize.TypeFlow + "." + label) {
			typ, l, _ := strings.Cut(c, ".")
			out = append(out, exporter.Resource{Address: exporter.Address{Type: typ, Label: l}, Name: name, ID: id})
		}
	}
	values := map[string]string{}
	for _, v := range vars {
		name, ctxName := str(v["name"]), str(v["context"])
		add(neutralize.TypeVariable, neutralize.Label(name, ctxName), name, str(v["id"]), v)
		// Every type but secret (masked by the API); terraformer writes
		// them all as strings (objects as JSON).
		if dt := str(v["dataType"]); dt != "secret" && v["value"] != nil {
			switch val := v["value"].(type) {
			case string:
				values[str(v["id"])] = val
			default:
				if b, err := json.Marshal(val); err == nil && !secretInside(val) {
					values[str(v["id"])] = string(b)
				}
			}
		}
	}
	for _, c := range conns {
		if !hexID.MatchString(str(c["id"])) {
			continue // built in (defaultUserPool, ...): terraformer does not export it
		}
		add(neutralize.TypeConnector, neutralize.Label(str(c["name"])), str(c["name"]), str(c["id"]), c)
	}
	for i, a := range apps {
		appID := str(a["id"])
		add("pingone_davinci_application", neutralize.Label(str(a["name"])), str(a["name"]), appID, a)
		for _, p := range policies[i] {
			add("pingone_davinci_application_flow_policy", neutralize.Label(str(p["name"])), str(p["name"]), appID+"/"+str(p["id"]), p)
		}
	}
	sort.Strings(fp)
	sum := sha256.Sum256([]byte(strings.Join(fp, "\n")))
	l.items, l.fp, l.values = out, hex.EncodeToString(sum[:]), values
	return l, nil
}

// hexID is a DaVinci object ID; built-in connector instances have names.
var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// toExport maps DaVinci addresses from the quick list onto the terraformer
// export's addresses by ID (they are the same unless a label differs).
// Addresses it cannot map are returned unchanged.
func toExport(addrs []string, fast, full []exporter.Resource) []string {
	if len(fast) == 0 || len(full) == 0 {
		return addrs
	}
	idOf := map[string]string{}
	for _, r := range fast {
		if r.ID != "" {
			idOf[r.String()] = r.Type + " " + r.ID
		}
	}
	byID := map[string]string{}
	for _, r := range full {
		if r.ID != "" {
			byID[r.Type+" "+r.ID] = r.String()
		}
	}
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a
		if k, ok := idOf[a]; ok {
			if to, ok := byID[k]; ok {
				out[i] = to
			}
		}
	}
	return out
}

// secretInside reports whether a JSON value holds a credential.
func secretInside(x any) bool { return secretscan.CredentialInJSON(x) }
