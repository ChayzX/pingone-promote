package promotion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
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
	flowLifecycle, flowLifecycleCandidate, flowLifecycleReason := selectedFlowVersionLifecycle(source, target, selected, mappings, steps, rollback)
	if len(steps) > 0 && flowLifecycleCandidate && flowLifecycle == nil {
		add("selected flow/version create lifecycle is unsupported or mismatched: " + flowLifecycleReason)
	}
	if len(steps) == 0 {
		add("promotion plan has no steps")
	}
	created := map[string]string{}
	for i, st := range steps {
		rev := StepReview{Action: st.Action, URL: st.URL, SourceID: st.Source.ID, TargetID: st.Target.ID}
		if flowLifecycle != nil && (i == 1 || i == 3) {
			// These native control steps have no sourceResource. Bind them to the
			// explicitly selected root flow in the user-visible review.
			rev.SourceID = flowLifecycle.rootID
		}
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
			if !rollback && !(flowLifecycle != nil && (i == 1 || i == 3)) {
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
		if rollback && act == "DELETE" && st.Target.ID != "" && !rollbackTargetMatches(u, st.Target.ID, dst) {
			rev.Issue = "rollback target does not match its resource URL"
			add(fmt.Sprintf("step %d: rollback target does not match its resource URL", i))
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
			if flowLifecycle != nil && i == 2 {
				// The narrowly validated selected-version create is the only
				// supported child-create operation in this lifecycle.
			} else if targetID != "" && hasTarget {
				if st.Target.ID != "" && st.Target.ID != targetID {
					rev.Issue = "step target does not match mapping"
					add(fmt.Sprintf("step %d: step target does not match mapping", i))
				}
				if !validMappedPath(u, tr, act) {
					rev.Issue = "target operation does not match mapped resource"
					add(fmt.Sprintf("step %d: target operation does not match mapped resource", i))
				}
			} else if !validCreatePath(u, sr, target, act) {
				rev.Issue = "unmapped operation is not a valid collection create"
				add(fmt.Sprintf("step %d: unmapped operation is not a valid collection create", i))
			} else {
				payloadID, _ := payloadObject["id"].(string)
				if st.Target.ID != "" && payloadID != "" && st.Target.ID != payloadID {
					rev.Issue = "created target identity differs from its payload"
					add(fmt.Sprintf("step %d: created target identity differs from its payload", i))
				} else if id := createdRootID(st, payload); id != "" {
					created[st.Source.ID] = id
				}
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

type flowVersionLifecycle struct {
	rootID    string
	versionID string
}

// selectedFlowVersionLifecycle recognizes only the observed native new-flow
// lifecycle. A selected flow and its explicitly selected version form a
// candidate; if any detail differs from this exact sequence, the candidate is
// rejected rather than falling back to generic collection-create rules.
func selectedFlowVersionLifecycle(source, target pingone.Inventory, selected []string, mappings map[string]string, steps []pingone.Step, rollback bool) (*flowVersionLifecycle, bool, string) {
	if rollback {
		return nil, false, ""
	}
	reject := func(reason string) (*flowVersionLifecycle, bool, string) { return nil, true, reason }
	src := resourceIndex(source)
	selectedSet := map[string]bool{}
	selectedRoots := []string{}
	selectedVersions := []string{}
	for _, id := range selected {
		selectedSet[id] = true
		if src[id].Type == "flows" {
			selectedRoots = append(selectedRoots, id)
		}
		if src[id].Type == "flows / versions / details" {
			selectedVersions = append(selectedVersions, id)
		}
	}
	if len(selectedRoots) == 0 || len(selectedVersions) == 0 {
		return nil, false, ""
	}
	candidate := true
	if len(selectedRoots) != 1 || len(selectedVersions) != 1 {
		return reject("selection-shape")
	}
	rootID, versionID := selectedRoots[0], selectedVersions[0]
	owner, _, ownerOK := flowVersionResourceIdentity(versionID)
	if !ownerOK || owner != rootID {
		return reject("selected-version-identity")
	}
	if len(selected) != 2 || len(selectedSet) != 2 || !selectedSet[rootID] || !selectedSet[versionID] {
		return reject("selection-shape")
	}
	if len(steps) != 4 {
		return reject("step-count")
	}
	if _, mapped := mappings[rootID]; mapped {
		return reject("root-mapping-unsupported")
	}
	if _, mapped := mappings[versionID]; mapped {
		return reject("version-mapping-unsupported")
	}
	root, rootExists := src[rootID]
	version, versionExists := src[versionID]
	if !rootExists || root.Type != "flows" || root.Name == "" || root.Configuration == nil {
		return reject("source-root-config")
	}
	if !versionExists || version.Type != "flows / versions / details" || version.Configuration == nil {
		return reject("source-version-config")
	}
	owner, versionNumber, validVersion := flowVersionResourceIdentity(versionID)
	if !validVersion || owner != rootID {
		return reject("selected-version-identity")
	}
	if !exactInventoryURL(root.URL, source.APIOrigin, source.EnvironmentID, "/flows/"+rootID) {
		return reject("source-root-url")
	}
	if !exactInventoryURL(version.URL, source.APIOrigin, source.EnvironmentID, "/flows/"+rootID+"/versions/"+strconv.FormatUint(versionNumber, 10)+"/details") {
		return reject("source-version-url")
	}
	if sourceRootID, _ := root.Configuration["id"].(string); sourceRootID != rootID {
		return reject("source-root-id")
	}
	if sourceName, _ := root.Configuration["name"].(string); sourceName != root.Name {
		return reject("source-root-name")
	}
	if root.Configuration["enabled"] != true {
		return reject("source-root-enabled")
	}
	sourceEnvironment, ok := root.Configuration["environment"].(map[string]any)
	if !ok || sourceEnvironment["id"] != source.EnvironmentID {
		return reject("source-root-environment")
	}
	sourceCurrent, currentOK := positiveSafeInteger(root.Configuration["currentVersion"])
	sourcePublished, publishedOK := positiveSafeInteger(root.Configuration["publishedVersion"])
	if !currentOK || sourceCurrent != versionNumber {
		return reject("source-root-current-version")
	}
	if !publishedOK || sourcePublished != versionNumber {
		return reject("source-root-published-version")
	}
	sourceVersionFlow, ok := version.Configuration["flow"].(map[string]any)
	if !ok || sourceVersionFlow["id"] != rootID {
		return reject("source-version-parent-id")
	}
	if sourceVersionFlow["name"] != root.Name {
		return reject("source-version-parent-name")
	}
	if sourceVersion, ok := positiveSafeInteger(version.Configuration["version"]); !ok || sourceVersion != versionNumber {
		return reject("source-version-number")
	}
	if source.EnvironmentID == "" || target.EnvironmentID == "" || source.EnvironmentID == target.EnvironmentID {
		return reject("environment-identity")
	}
	if source.APIOrigin == "" || target.APIOrigin == "" {
		return reject("api-origin")
	}
	for _, resource := range target.Resources {
		if resource.Type == "flows" && resource.ID == rootID {
			return reject("target-root-id-collision")
		}
		if resource.Type == "flows" && resource.Name == root.Name {
			return reject("target-root-name-collision")
		}
		if resource.ID == versionID {
			return reject("target-version-id-collision")
		}
	}
	if steps[0].Action != "POST" {
		return reject("root-step-method")
	}
	if steps[0].Source.ID != rootID {
		return reject("root-step-source-ref")
	}
	// Source.Name is optional display metadata, not an identity check. The
	// complete raw step, including this field, remains bound by the digest.
	if !emptyRef(steps[0].Target) {
		return reject("root-step-target-ref")
	}
	if !exactInventoryURL(steps[0].URL, target.APIOrigin, target.EnvironmentID, "/flows") {
		return reject("root-step-target-url")
	}
	if steps[1].Action != "PUT" {
		return reject("enabled-step-method")
	}
	if !emptyRef(steps[1].Source) || !emptyRef(steps[1].Target) {
		return reject("enabled-step-control-refs")
	}
	if !exactInventoryURL(steps[1].URL, target.APIOrigin, target.EnvironmentID, "/flows/"+rootID+"/enabled") {
		return reject("enabled-step-target-url")
	}
	if steps[2].Action != "POST" {
		return reject("version-step-method")
	}
	if steps[2].Source.ID != versionID {
		return reject("version-step-source-ref")
	}
	if !emptyRef(steps[2].Target) {
		return reject("version-step-target-ref")
	}
	if !exactInventoryURL(steps[2].URL, target.APIOrigin, target.EnvironmentID, "/flows/"+rootID+"/versions") {
		return reject("version-step-target-url")
	}
	if steps[3].Action != "POST" {
		return reject("publish-step-method")
	}
	if !emptyRef(steps[3].Source) || !emptyRef(steps[3].Target) {
		return reject("publish-step-control-refs")
	}
	if !exactInventoryURL(steps[3].URL, target.APIOrigin, target.EnvironmentID, "/flows/"+rootID+"/publishedVersion") {
		return reject("publish-step-target-url")
	}
	rootPayload, ok := strictJSONObject(steps[0].Payload)
	if !ok || !exactObjectKeys(rootPayload, "color", "connectors", "currentVersion", "deployedAt", "description", "enabled", "environment", "graphData", "id", "name", "publishedVersion", "settings") {
		return reject("root-payload-schema")
	}
	if rootPayload["id"] != rootID {
		return reject("root-payload-id")
	}
	if rootPayload["name"] != root.Name {
		return reject("root-payload-name")
	}
	if rootPayload["enabled"] != true {
		return reject("root-payload-enabled")
	}
	currentVersion, currentOK := positiveSafeInteger(rootPayload["currentVersion"])
	publishedVersion, publishedOK := positiveSafeInteger(rootPayload["publishedVersion"])
	if !currentOK || currentVersion != versionNumber {
		return reject("root-payload-current-version")
	}
	if !publishedOK || publishedVersion != versionNumber {
		return reject("root-payload-published-version")
	}
	environment, ok := rootPayload["environment"].(map[string]any)
	if !ok || !exactObjectKeys(environment, "id") || environment["id"] != target.EnvironmentID {
		return reject("root-payload-environment")
	}
	enabledPayload, ok := strictJSONObject(steps[1].Payload)
	if !ok || !exactObjectKeys(enabledPayload, "enabled") || enabledPayload["enabled"] != true {
		return reject("enabled-payload-controls")
	}
	versionPayload, ok := strictJSONObject(steps[2].Payload)
	if !ok || !exactObjectKeys(versionPayload, "color", "connectors", "deployedAt", "description", "enabled", "flow", "graphData", "id", "settings", "updates", "version") {
		return reject("version-payload-schema")
	}
	versionFlow, ok := versionPayload["flow"].(map[string]any)
	if !ok || !exactObjectKeys(versionFlow, "id", "name") {
		return reject("version-payload-parent-schema")
	}
	if versionFlow["id"] != rootID || versionPayload["id"] != versionID {
		return reject("version-payload-identity")
	}
	if versionFlow["name"] != root.Name {
		return reject("version-payload-parent-name")
	}
	if versionPayload["enabled"] != true {
		return reject("version-payload-enabled")
	}
	payloadVersion, ok := positiveSafeInteger(versionPayload["version"])
	if !ok || payloadVersion != versionNumber {
		return reject("version-payload-number")
	}
	publishPayload, ok := strictJSONObject(steps[3].Payload)
	if !ok || !exactObjectKeys(publishPayload, "version") || publishPayload["version"] != strconv.FormatUint(versionNumber, 10) {
		return reject("publish-payload-controls")
	}
	return &flowVersionLifecycle{rootID: rootID, versionID: versionID}, candidate, ""
}

func flowVersionResourceIdentity(id string) (string, uint64, bool) {
	if !strings.HasPrefix(id, "flowId:") {
		return "", 0, false
	}
	separator := strings.LastIndex(id, "-version:")
	if separator <= len("flowId:") {
		return "", 0, false
	}
	owner := id[len("flowId:"):separator]
	versionText := id[separator+len("-version:"):]
	version, err := strconv.ParseUint(versionText, 10, 64)
	if err != nil || version == 0 || version > 9007199254740991 || strconv.FormatUint(version, 10) != versionText || !pingone.ValidResourceID(owner) {
		return "", 0, false
	}
	return owner, version, true
}

func exactInventoryURL(raw, apiOrigin, environment, resourcePath string) bool {
	u, err := url.Parse(raw)
	base, baseErr := url.Parse(apiOrigin)
	if err != nil || baseErr != nil || u == nil || base == nil || u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || base.User != nil || base.RawPath != "" || base.RawQuery != "" || base.Fragment != "" {
		return false
	}
	if u.Scheme != "https" || base.Scheme != "https" || base.Host == "" || base.Path != "" && base.Path != "/" || !strings.EqualFold(u.Host, base.Host) || u.Scheme != base.Scheme {
		return false
	}
	return u.Path == pingone.EnvPath(environment)+resourcePath
}

func emptyRef(ref pingone.Ref) bool { return ref.ID == "" && ref.Name == "" }

func strictJSONObject(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if decoder.Decode(&object) != nil || object == nil {
		return nil, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, false
	}
	return object, true
}

func exactObjectKeys(object map[string]any, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func positiveSafeInteger(value any) (uint64, bool) {
	const maxSafeInteger = uint64(9007199254740991)
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseUint(number.String(), 10, 64)
		return parsed, err == nil && parsed > 0 && parsed <= maxSafeInteger
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 1 || number > float64(maxSafeInteger) || math.Trunc(number) != number {
			return 0, false
		}
		return uint64(number), true
	case int:
		return uint64(number), number > 0 && uint64(number) <= maxSafeInteger
	case int64:
		return uint64(number), number > 0 && uint64(number) <= maxSafeInteger
	case uint64:
		return number, number > 0 && number <= maxSafeInteger
	default:
		return 0, false
	}
}

func rollbackTargetMatches(u *url.URL, targetID string, dst map[string]pingone.Resource) bool {
	if target, ok := dst[targetID]; ok {
		ru, err := url.Parse(target.URL)
		if err != nil || !strings.EqualFold(ru.Scheme, u.Scheme) || !strings.EqualFold(ru.Host, u.Host) {
			return false
		}
		root := strings.TrimRight(ru.Path, "/")
		return u.Path == root || strings.HasPrefix(u.Path, root+"/")
	}
	parts, ok := pathFor(u.String())
	if !ok {
		return false
	}
	for i := 4; i < len(parts); i += 2 {
		if parts[i] == targetID {
			return true
		}
	}
	return false
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
		markedSensitive := false
		for k, val := range x {
			switch redactionKey(k) {
			case "issensitive":
				markedSensitive = markedSensitive || val == true
			case "datatype":
				if kind, ok := val.(string); ok && strings.EqualFold(kind, "secret") {
					markedSensitive = true
				}
			}
		}
		for k, val := range x {
			lk := redactionKey(k)
			if lk == "issensitive" {
				out[k] = val
				continue
			}
			if strings.Contains(lk, "password") || strings.Contains(lk, "passphrase") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") || strings.Contains(lk, "key") || strings.Contains(lk, "credential") || strings.Contains(lk, "authentication") || strings.Contains(lk, "authorization") || strings.Contains(lk, "connectionstring") || strings.Contains(lk, "connectionuri") || lk == "dsn" || lk == "auth" || lk == "value" && markedSensitive {
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

func redactionKey(key string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
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
	return false
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
