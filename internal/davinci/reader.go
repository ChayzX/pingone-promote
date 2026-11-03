package davinci

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pingone-promote/internal/pingone"
)

// MaskedValue is how the API returns a stored secret.
const MaskedValue = "******"

var idRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// API is the subset of *pingone.Client the reader uses. It only issues GETs.
type API interface {
	Do(ctx context.Context, method, path, contentType string, body, output any) error
	Pages(ctx context.Context, path, key string) ([]json.RawMessage, error)
}

var _ API = (*pingone.Client)(nil)

// Read builds an inventory of envID. It never fails as a whole: each kind
// that cannot be read, or is read only in part, is recorded in Partial with a
// classified reason, and the rest is returned. Read issues only GET requests.
func Read(ctx context.Context, api API, envID string) *Inventory {
	inv := &Inventory{EnvironmentID: envID}
	if !idRE.MatchString(envID) {
		for _, k := range []Kind{KindFlows, KindConnections, KindVariables, KindApplications, KindFlowPolicies} {
			inv.Partial = append(inv.Partial, Failure{Kind: k, Reason: ReasonMalformed})
		}
		return inv
	}
	env := pingone.EnvPath(envID)
	r := &reader{ctx: ctx, api: api, inv: inv}

	inv.Flows = r.flows(env)
	inv.Connections = list[ConnectorInstance](r, KindConnections, env+"/connectorInstances", "connectorInstances", func(c ConnectorInstance) string { return c.ID })
	inv.Variables = list[Variable](r, KindVariables, env+"/variables", "variables", func(v Variable) string { return v.ID })
	inv.Applications = list[Application](r, KindApplications, env+"/davinciApplications", "davinciApplications", func(a Application) string { return a.ID })
	if !inv.Complete(KindApplications) {
		// Policies of unlisted applications are unknown.
		r.fail(KindFlowPolicies, ReasonIncomplete, 0, 0)
	}
	failedApps := 0
	var lastFail Failure
	for _, app := range inv.Applications {
		pols, f := listOne[FlowPolicy](r, env+"/davinciApplications/"+url.PathEscape(app.ID)+"/flowPolicies", "flowPolicies", func(p FlowPolicy) string { return p.ID })
		if f != nil {
			failedApps++
			lastFail = *f
			continue
		}
		for i := range pols {
			pols[i].ApplicationID = app.ID
		}
		inv.FlowPolicies = append(inv.FlowPolicies, pols...)
	}
	if failedApps > 0 {
		r.fail(KindFlowPolicies, lastFail.Reason, lastFail.Status, failedApps)
	}
	sort.Slice(inv.FlowPolicies, func(i, j int) bool {
		a, b := inv.FlowPolicies[i], inv.FlowPolicies[j]
		if a.ApplicationID != b.ApplicationID {
			return a.ApplicationID < b.ApplicationID
		}
		return a.ID < b.ID
	})
	return inv
}

type reader struct {
	ctx context.Context
	api API
	inv *Inventory
}

func (r *reader) fail(kind Kind, reason Reason, status, count int) {
	r.inv.Partial = append(r.inv.Partial, Failure{Kind: kind, Reason: reason, Status: status, Count: count})
}

// Classify maps a client error to a Reason and HTTP status.
func Classify(err error) (Reason, int) {
	var he *pingone.HTTPError
	if errors.As(err, &he) {
		switch {
		case he.Status == 401:
			return ReasonUnauthorized, he.Status
		case he.Status == 403:
			return ReasonForbidden, he.Status
		case he.Status == 404:
			return ReasonNotFound, he.Status
		case he.Status == 429:
			return ReasonRateLimited, he.Status
		case he.Status >= 500:
			return ReasonServer, he.Status
		}
		return ReasonHTTP, he.Status
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "pagination"):
		return ReasonIncomplete, 0
	case strings.Contains(msg, "OAuth") || strings.Contains(msg, "credential"):
		return ReasonCredential, 0
	case strings.Contains(msg, "malformed") || strings.Contains(msg, "incomplete PingOne response"):
		return ReasonMalformed, 0
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return ReasonMalformed, 0
	}
	return ReasonTransport, 0
}

// listOne pages a collection and decodes it, rejecting invalid or duplicate
// IDs. It returns a failure instead of recording it.
func listOne[T any](r *reader, path, key string, id func(T) string) ([]T, *Failure) {
	raws, err := r.api.Pages(r.ctx, path, key)
	if err != nil {
		reason, status := Classify(err)
		return nil, &Failure{Reason: reason, Status: status}
	}
	out := make([]T, 0, len(raws))
	seen := map[string]bool{}
	for _, raw := range raws {
		var item T
		if err := json.Unmarshal(raw, &item); err != nil || !idRE.MatchString(id(item)) || seen[id(item)] {
			return nil, &Failure{Reason: ReasonMalformed}
		}
		seen[id(item)] = true
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return id(out[i]) < id(out[j]) })
	return out, nil
}

func list[T any](r *reader, kind Kind, path, key string, id func(T) string) []T {
	items, f := listOne(r, path, key, id)
	if f != nil {
		r.fail(kind, f.Reason, f.Status, 0)
		return nil
	}
	return items
}

// flows lists flows, then fetches each one for its full graph.
func (r *reader) flows(env string) []Flow {
	summaries, f := listOne(r, env+"/flows", "flows", func(fl Flow) string { return fl.ID })
	if f != nil {
		r.fail(KindFlows, f.Reason, f.Status, 0)
		return nil
	}
	out := make([]Flow, 0, len(summaries))
	failed := 0
	var last Failure
	for _, s := range summaries {
		var full Flow
		err := r.api.Do(r.ctx, "GET", env+"/flows/"+url.PathEscape(s.ID), "", nil, &full)
		if err == nil && full.ID != s.ID {
			err = errors.New("malformed: flow identity did not match request")
		}
		if err != nil {
			failed++
			reason, status := Classify(err)
			last = Failure{Reason: reason, Status: status}
			continue
		}
		out = append(out, full)
	}
	if failed > 0 {
		r.fail(KindFlows, last.Reason, last.Status, failed)
	}
	return out
}

// MaskedPaths lists the JSON paths ("/prop/value") of masked values in raw.
func MaskedPaths(raw json.RawMessage) []string {
	var tree any
	if len(raw) == 0 || json.Unmarshal(raw, &tree) != nil {
		return nil
	}
	var out []string
	var walk func(string, any)
	walk = func(p string, v any) {
		switch x := v.(type) {
		case string:
			if x == MaskedValue {
				out = append(out, p)
			}
		case []any:
			for i, e := range x {
				walk(p+"/"+strconv.Itoa(i), e)
			}
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(p+"/"+k, x[k])
			}
		}
	}
	walk("", tree)
	return out
}
