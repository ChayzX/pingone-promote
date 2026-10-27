package promotion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"pingone-promote/internal/pingone"
)

type StepReview struct {
	Action   string   `json:"action"`
	URL      string   `json:"url"`
	SourceID string   `json:"sourceId,omitempty"`
	TargetID string   `json:"targetId,omitempty"`
	Name     string   `json:"name,omitempty"`
	Before   any      `json:"before,omitempty"`
	After    any      `json:"after,omitempty"`
	Changes  []string `json:"changes,omitempty"`
	Issue    string   `json:"issue,omitempty"`
}
type Review struct {
	Digest string       `json:"digest"`
	Issues []string     `json:"issues"`
	Steps  []StepReview `json:"steps"`
}

func BuildRequest(source, target pingone.Inventory, selected []string, mappings map[string]string, description string) (map[string]any, error) {
	if len(selected) == 0 {
		return nil, fmt.Errorf("selection must not be empty")
	}
	if source.EnvironmentID == "" || target.EnvironmentID == "" || source.EnvironmentID == target.EnvironmentID {
		return nil, fmt.Errorf("source and target environments must be distinct and known")
	}
	if source.SnapshotID == "" {
		return nil, fmt.Errorf("source snapshot must be pinned")
	}
	if source.OrganizationID == "" || target.OrganizationID == "" || source.OrganizationID != target.OrganizationID {
		return nil, fmt.Errorf("source and target must belong to the same organization")
	}
	src, dst := resourceIndex(source), resourceIndex(target)
	selectedSet := map[string]bool{}
	for _, id := range selected {
		if _, ok := src[id]; !ok {
			return nil, fmt.Errorf("unknown source resource %q", id)
		}
		if selectedSet[id] {
			return nil, fmt.Errorf("duplicate selected resource %q", id)
		}
		selectedSet[id] = true
	}
	seenTargets := map[string]string{}
	for sid, tid := range mappings {
		s, ok := src[sid]
		if !ok {
			return nil, fmt.Errorf("mapping source %q is unknown", sid)
		}
		d, ok := dst[tid]
		if !ok {
			return nil, fmt.Errorf("mapping target %q is unknown", tid)
		}
		if s.Type != d.Type {
			return nil, fmt.Errorf("mapping %q to %q has incompatible resource types", sid, tid)
		}
		if prior, ok := seenTargets[tid]; ok && prior != sid {
			return nil, fmt.Errorf("multiple sources map to target %q", tid)
		}
		seenTargets[tid] = sid
	}
	excluded := []string{}
	for id := range src {
		if !selectedSet[id] {
			excluded = append(excluded, id)
		}
	}
	sort.Strings(excluded)
	sel := append([]string(nil), selected...)
	sort.Strings(sel)
	return map[string]any{"sourceSnapshot": map[string]string{"id": source.SnapshotID}, "targetEnvironment": map[string]string{"id": target.EnvironmentID}, "selectedResources": sel, "excludedResources": excluded, "resourceMapping": mappings, "description": description}, nil
}

func Inspect(source, target pingone.Inventory, selected []string, mappings map[string]string, steps []pingone.Step, rollback bool) Review {
	r := Review{Issues: []string{}, Steps: []StepReview{}}
	src, dst := resourceIndex(source), resourceIndex(target)
	approved := map[string]bool{}
	for _, id := range selected {
		approved[id] = true
	}
	add := func(issue string) { r.Issues = append(r.Issues, issue) }
	if len(steps) == 0 {
		add("promotion plan has no steps")
	}
	created := map[string]string{}
	for i, st := range steps {
		rev := StepReview{Action: st.Action, URL: st.URL, SourceID: st.Source.ID, TargetID: st.Target.ID}
		payload, err := decode(st.Payload)
		if err != nil {
			rev.Issue = "malformed step payload"
			add(fmt.Sprintf("step %d: malformed payload", i))
		}
		payloadObject, objectPayload := payload.(map[string]any)
		bodylessFlowAction := len(st.Payload) == 0 && !rollback && strings.EqualFold(st.Action, "POST") && st.Source.ID != "" && src[st.Source.ID].Type == "flows" && mappings[st.Source.ID] != "" && dst[mappings[st.Source.ID]].Type == "flows" && exactURL(st.URL, dst[mappings[st.Source.ID]].URL)
		bodylessRollbackDelete := len(st.Payload) == 0 && rollback && strings.EqualFold(st.Action, "DELETE")
		if err == nil && !objectPayload && !bodylessFlowAction && !bodylessRollbackDelete {
			rev.Issue = "uninspectable step payload"
			add(fmt.Sprintf("step %d: payload must be a JSON object", i))
		}
		if objectPayload {
			m := payloadObject
			rev.Name, _ = m["name"].(string)
			rev.After = Redact(m)
		}
		u, err := url.Parse(st.URL)
		if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			rev.Issue = "malformed or unsafe URL"
			add(fmt.Sprintf("step %d: malformed or unsafe URL", i))
			r.Steps = append(r.Steps, rev)
			continue
		}
		if st.Source.ID == "" {
			if !rollback {
				rev.Issue = "missing source resource"
				add(fmt.Sprintf("step %d: missing source resource", i))
			}
		} else if !rollback && (!approved[st.Source.ID] || src[st.Source.ID].ID == "") {
			rev.Issue = "source resource is outside selection"
			add(fmt.Sprintf("step %d: source %q is unknown or unselected", i, st.Source.ID))
		}
		act := strings.ToUpper(st.Action)
		if act != "POST" && act != "PUT" && act != "PATCH" && !(rollback && act == "DELETE") {
			rev.Issue = "unsupported action"
			add(fmt.Sprintf("step %d: unsupported action %q", i, st.Action))
		}
		if !sameOriginEnv(u, target, dst) {
			rev.Issue = "target URL is outside destination environment"
			add(fmt.Sprintf("step %d: target URL is outside destination environment", i))
		}
		var before map[string]any
		if tr, ok := dst[st.Target.ID]; ok {
			before = tr.Configuration
		}
		if pm, ok := payload.(map[string]any); ok {
			rev.Before = Redact(before)
			rev.Changes = diffFields(before, pm, "")
		}
		if !rollback && st.Source.ID != "" && src[st.Source.ID].ID != "" {
			sr := src[st.Source.ID]
			targetID := mappings[st.Source.ID]
			tr, hasTarget := dst[targetID]
			if targetID != "" && !hasTarget {
				rev.Issue = "mapping target is unknown"
				add(fmt.Sprintf("step %d: mapping target is unknown", i))
			}
			if targetID != "" && hasTarget && tr.Type != sr.Type {
				rev.Issue = "mapping resource type mismatch"
				add(fmt.Sprintf("step %d: mapping resource type mismatch", i))
			}
			if targetID != "" && hasTarget {
				if st.Target.ID != "" && st.Target.ID != targetID {
					rev.Issue = "step target does not match mapping"
					add(fmt.Sprintf("step %d: step target does not match mapping", i))
				}
				if !validMappedPath(u, tr, act) {
					rev.Issue = "target operation does not match mapped resource"
					add(fmt.Sprintf("step %d: target operation does not match mapped resource", i))
				}
			} else if !validCreatePath(u, sr, target, act) {
				if !validCreatedFlowChild(u, sr, created[st.Source.ID], act) {
					rev.Issue = "unmapped operation is not a valid collection create"
					add(fmt.Sprintf("step %d: unmapped operation is not a valid collection create", i))
				}
			} else if id := createdRootID(st, payload); id != "" {
				created[st.Source.ID] = id
			}
		}
		r.Steps = append(r.Steps, rev)
	}
	// Canonical full input, including unredacted payload, participates in the server-side review fingerprint.
	canonical, _ := json.Marshal(struct {
		Source, Target string
		Selected       []string
		Mappings       map[string]string
		Rollback       bool
		Steps          []pingone.Step
	}{source.EnvironmentID + "/" + source.SnapshotID, target.EnvironmentID, selected, mappings, rollback, steps})
	h := sha256.Sum256(canonical)
	r.Digest = hex.EncodeToString(h[:])
	return r
}

func resourceIndex(inv pingone.Inventory) map[string]pingone.Resource {
	m := map[string]pingone.Resource{}
	for _, r := range inv.Resources {
		m[r.ID] = r
	}
	return m
}
func decode(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	err := json.Unmarshal(raw, &v)
	return v, err
}
func exactURL(a, b string) bool {
	ua, e1 := url.Parse(a)
	ub, e2 := url.Parse(b)
	return e1 == nil && e2 == nil && ua.Scheme == ub.Scheme && strings.EqualFold(ua.Host, ub.Host) && ua.Path == ub.Path && ua.RawQuery == "" && ub.RawQuery == "" && ua.Fragment == "" && ub.Fragment == ""
}
func Redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			lk := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "_", ""), "-", ""))
			if lk == "issensitive" {
				out[k] = val
				continue
			}
			if strings.Contains(lk, "password") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") || strings.Contains(lk, "key") || strings.Contains(lk, "credential") || strings.Contains(lk, "authentication") || lk == "auth" || lk == "value" && x["isSensitive"] == true {
				out[k] = "[REDACTED]"
			} else {
				out[k] = Redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = Redact(v)
		}
		return out
	default:
		return v
	}
}
func pathFor(raw string) ([]string, bool) {
	u, e := url.Parse(raw)
	if e != nil || u.RawPath != "" || strings.Contains(raw, "%") || strings.Contains(u.Path, "//") {
		return nil, false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	for _, part := range p {
		if part == "." || part == ".." || part == "" {
			return nil, false
		}
	}
	return p, len(p) >= 4 && p[0] == "v1" && p[1] == "environments"
}
func sameOriginEnv(u *url.URL, target pingone.Inventory, dst map[string]pingone.Resource) bool {
	p, ok := pathFor(u.String())
	if !ok || p[2] != target.EnvironmentID {
		return false
	}
	if target.APIOrigin != "" {
		origin, e := url.Parse(target.APIOrigin)
		return e == nil && strings.EqualFold(origin.Scheme, u.Scheme) && strings.EqualFold(origin.Host, u.Host)
	}
	for _, r := range dst {
		ru, e := url.Parse(r.URL)
		if e == nil && strings.EqualFold(ru.Scheme, u.Scheme) && strings.EqualFold(ru.Host, u.Host) {
			return true
		}
	}
	return false
}
func validMappedPath(u *url.URL, r pingone.Resource, action string) bool {
	ru, e := url.Parse(r.URL)
	if e != nil || !strings.EqualFold(ru.Host, u.Host) || ru.Scheme != u.Scheme {
		return false
	}
	want := strings.TrimRight(ru.Path, "/")
	got := strings.TrimRight(u.Path, "/")
	if got == want {
		return action == "PUT" || action == "PATCH" || action == "POST"
	}
	return r.Type == "flows" && action == "POST" && strings.HasPrefix(got, want+"/versions") && (got == want+"/versions" || strings.HasPrefix(got, want+"/versions/"))
}
func validCreatePath(u *url.URL, s pingone.Resource, target pingone.Inventory, action string) bool {
	if action != "POST" {
		return false
	}
	su, e := url.Parse(s.URL)
	if e != nil {
		return false
	}
	sp := strings.TrimRight(su.Path, "/")
	slash := strings.LastIndex(sp, "/")
	if slash < 0 {
		return false
	}
	expected := sp[:slash]
	p := strings.Trim(strings.TrimRight(u.Path, "/"), "/")
	parts := strings.Split(strings.Trim(expected, "/"), "/")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != "environments" {
		return false
	}
	if parts[3] != s.Type || !strings.EqualFold(su.Scheme, u.Scheme) || !strings.EqualFold(su.Host, u.Host) {
		return false
	}
	parts[2] = target.EnvironmentID
	return p == strings.Join(parts, "/")
}

func createdRootID(st pingone.Step, payload any) string {
	if st.Target.ID != "" {
		return st.Target.ID
	}
	if m, ok := payload.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			return id
		}
	}
	return ""
}
func validCreatedFlowChild(u *url.URL, s pingone.Resource, id, action string) bool {
	if s.Type != "flows" || id == "" || (action != "POST" && action != "PUT" && action != "PATCH") {
		return false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(p) >= 6 && p[0] == "v1" && p[1] == "environments" && p[3] == "flows" && p[4] == id && p[5] == "versions" && (len(p) == 6 || p[6] != "")
}

func diffFields(before, after map[string]any, prefix string) []string {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	changes := []string{}
	for _, k := range ordered {
		a, aok := before[k]
		b, bok := after[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if aok && bok {
			am, aMap := a.(map[string]any)
			bm, bMap := b.(map[string]any)
			if aMap && bMap {
				changes = append(changes, diffFields(am, bm, path)...)
				continue
			}
			aj, _ := json.Marshal(a)
			bj, _ := json.Marshal(b)
			if string(aj) == string(bj) {
				continue
			}
		}
		changes = append(changes, path)
	}
	return changes
}
