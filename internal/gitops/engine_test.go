package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"pingone-promote/internal/pingone"
	"strings"
	"testing"
	"time"
)

type nativeFixture struct {
	origin                    string
	client                    *pingone.Client
	req                       Request
	steps                     []pingone.Step
	native                    pingone.Promotion
	execute, rollback, create int
	mutatedSource             bool
	targetChanged             bool
	targetDependencyChanged   bool
	targetCollision           bool
	reuseDependency           bool
	targetUnavailable         bool
	targetDuplicate           bool
	rollbackLinkMode          string
	page2                     bool
	omitTarget                bool
	omitTargetSnapshot        bool
	seenBody                  map[string]any
	// liveTarget, when set, alters the target inventory served for any fresh
	// (non-pinned) target snapshot, simulating console edits after review.
	liveTarget     func([]pingone.Resource) []pingone.Resource
	liveTargetFail bool
}

func fixture(t *testing.T) *nativeFixture {
	t.Helper()
	f := &nativeFixture{}
	s := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	f.origin = s.URL
	f.client = &pingone.Client{BaseURL: s.URL, HTTP: s.Client(), AccessToken: "test"}
	src, dst := testInventories(s.URL)
	var err error
	f.req, err = NewRequest(src, dst, "us", "us", []string{"flow"}, map[string]string{"flow": "dest"}, "review")
	if err != nil {
		t.Fatal(err)
	}
	f.native = pingone.Promotion{ID: "native", Status: "READY", Source: pingone.Ref{ID: "source"}, Target: pingone.Ref{ID: "target"}, SourceSnapshot: pingone.Ref{ID: "sourcepin"}, TargetSnapshot: pingone.Ref{ID: "targetpin"}, Mapping: map[string]string{"flow": "dest"}}
	f.steps = []pingone.Step{{Action: "PUT", URL: s.URL + "/v1/environments/target/flows/dest", Source: pingone.Ref{ID: "flow"}, Target: pingone.Ref{ID: "dest"}, Payload: json.RawMessage(`{"name":"Flow","clientSecret":"secret-value"}`)}}
	return f
}
func (f *nativeFixture) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.Contains(path, "/promotions") {
		if strings.HasSuffix(path, "/promotionSteps") {
			if f.native.Status == "PREPARING" {
				http.Error(w, "not ready", 409)
				return
			}
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprintf(w, `{"_embedded":{"promotionSteps":[{"action":"POST","resourceUrl":%q,"sourceResource":{"id":"template"},"payload":{"name":"Unapproved"}}]}}`, f.origin+"/v1/environments/target/templates")
				return
			}
			out := map[string]any{"_embedded": map[string]any{"promotionSteps": f.steps}}
			if f.page2 {
				out["_links"] = map[string]any{"next": map[string]string{"href": "?page=2"}}
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method == "POST" {
			switch r.Header.Get("Content-Type") {
			case pingone.ExecuteType:
				f.execute++
				if f.native.ID == "rollback" {
					f.native.Status = "COMPLETED"
				} else {
					f.native.Status = "IN_PROGRESS"
				}
			case pingone.RollbackType:
				f.rollback++
				f.native.ID = "rollback"
				f.native.Previous.ID = "native"
				if f.rollbackLinkMode == "missing" {
					f.native.Previous.ID = ""
				} else if f.rollbackLinkMode == "wrong" {
					f.native.Previous.ID = "other"
				}
				f.native.Status = "READY"
				f.steps = []pingone.Step{{Action: "DELETE", URL: f.origin + "/v1/environments/target/flows/dest", Target: pingone.Ref{ID: "dest"}}}
			default:
				f.create++
				_ = json.NewDecoder(r.Body).Decode(&f.seenBody)
			}
		}
		p := f.native
		if f.omitTarget {
			p.Target.ID = ""
		}
		if f.omitTargetSnapshot {
			p.TargetSnapshot.ID = ""
		}
		json.NewEncoder(w).Encode(p)
		return
	}
	env := "source"
	pin := "sourcepin"
	if strings.HasPrefix(path, "/v1/environments/target") {
		env = "target"
		pin = "targetpin"
	}
	if strings.Contains(path, "/versions/target-current") {
		pin = "target-current"
	}
	if strings.HasSuffix(path, "/embeddedSnapshots") {
		src, dst := testInventories(f.origin)
		currentTargetSnapshot := strings.Contains(path, "/versions/target-current/")
		inv := src
		if env == "target" {
			inv = dst
		}
		resources := inv.Resources
		if env == "target" && f.liveTarget != nil && currentTargetSnapshot {
			resources = f.liveTarget(resources)
		}
		items := []any{}
		for _, res := range resources {
			if f.mutatedSource && res.ID == "flow" {
				res.Configuration["name"] = "Changed"
			}
			if env == "target" && currentTargetSnapshot && f.targetChanged && res.ID == "dest" {
				res.Configuration["name"] = "Changed after planning"
			}
			if env == "target" && currentTargetSnapshot && f.targetDependencyChanged && res.ID == "template" {
				res.Configuration["name"] = "Dependency changed after planning"
			}
			item := map[string]any{"resource": map[string]string{"id": res.ID}, "resourceUrl": res.URL, "configuration": res.Configuration}
			if env == "source" && f.reuseDependency && res.ID == "flow" {
				item["referencedResources"] = []string{"template"}
			}
			items = append(items, item)
			if env == "target" && currentTargetSnapshot && f.targetDuplicate && res.ID == "dest" {
				items = append(items, map[string]any{"resource": map[string]string{"id": res.ID}, "resourceUrl": res.URL, "configuration": res.Configuration})
			}
		}
		if env == "target" && f.reuseDependency {
			dependencyName := "Shared template"
			if currentTargetSnapshot && f.targetDependencyChanged {
				dependencyName = "Dependency changed after planning"
			}
			items = append(items, map[string]any{"resource": map[string]string{"id": "template"}, "resourceUrl": f.origin + "/v1/environments/target/templates/template", "configuration": map[string]any{"name": dependencyName}})
		}
		if env == "target" && currentTargetSnapshot && f.targetCollision {
			items = append(items, map[string]any{"resource": map[string]string{"id": "new-flow"}, "resourceUrl": f.origin + "/v1/environments/target/flows/new-flow", "configuration": map[string]any{"name": "Flow"}})
		}
		json.NewEncoder(w).Encode(map[string]any{"_embedded": map[string]any{"referencedResources": items}})
		return
	}
	if strings.Contains(path, "/snapshots") {
		if r.Method == "POST" && env != "target" {
			http.Error(w, "source pin bypass", 400)
			return
		}
		if r.Method == "POST" && env == "target" && (f.targetUnavailable || f.liveTargetFail) {
			http.Error(w, "target snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method == "POST" && env == "target" {
			json.NewEncoder(w).Encode(map[string]any{"snapshotId": "target-current", "resource": map[string]string{"id": env}, "status": "COMPLETE"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"snapshotId": pin, "resource": map[string]string{"id": env}, "status": "COMPLETE"})
		return
	}
	fmt.Fprintf(w, `{"id":%q,"organization":{"id":"org"}}`, env)
}
func testOrigin() Origin {
	return Origin{ServerURL: "https://gitlab.example.com", ProjectID: "123", PipelineID: "456", CommitSHA: strings.Repeat("a", 40)}
}
func TestPlanPinnedExplicitSelectionAndRedactedArtifact(t *testing.T) {
	f := fixture(t)
	a, err := (&Engine{Client: f.client}).Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Review.Issues) > 0 {
		t.Fatal(a.Review.Issues)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "secret-value") {
		t.Fatal("secret persisted in artifact")
	}
	if f.seenBody["targetEnvironment"].(map[string]any)["id"] != "target" {
		t.Fatal("missing target")
	}
	excluded := f.seenBody["excludedResources"].([]any)
	if len(excluded) != 1 || excluded[0] != "template" {
		t.Fatal("missing explicit exclusion")
	}
}
func TestPlanRejectsChangedPinnedSourceBeforeDraft(t *testing.T) {
	f := fixture(t)
	f.mutatedSource = true
	if _, err := (&Engine{Client: f.client}).Plan(context.Background(), f.req, testOrigin()); err == nil || f.create != 0 {
		t.Fatalf("changed source accepted: %v create=%d", err, f.create)
	}
}
func TestFullPageAndNativeIdentityBlock(t *testing.T) {
	for _, mutate := range []func(*nativeFixture){func(f *nativeFixture) { f.page2 = true }, func(f *nativeFixture) { f.omitTarget = true }} {
		f := fixture(t)
		mutate(f)
		a, e := (&Engine{Client: f.client}).Plan(context.Background(), f.req, testOrigin())
		if e == nil && len(a.Review.Issues) == 0 {
			t.Fatal("unsafe plan accepted")
		}
		if f.execute != 0 {
			t.Fatal("plan executed")
		}
	}
}
func TestApplyRequiresFreshAuthorizationDigestAndReadyPlan(t *testing.T) {
	for _, mode := range []string{"noauth", "wrongdigest", "changedstep", "changedmapping", "addedmapping", "missingmapping", "changedsourcepin", "started", "denied", "ok"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			digest := a.Review.Digest
			called := false
			auth := func(context.Context, Artifact) error {
				called = true
				if mode == "denied" {
					return fmt.Errorf("denied")
				}
				return nil
			}
			switch mode {
			case "noauth":
				auth = nil
			case "wrongdigest":
				digest = strings.Repeat("b", 64)
			case "changedstep":
				f.steps[0].Payload = json.RawMessage(`{"name":"changed"}`)
			case "changedmapping":
				f.native.Mapping["flow"] = "other"
			case "addedmapping":
				f.native.Mapping["extra"] = "extra"
			case "missingmapping":
				delete(f.native.Mapping, "flow")
			case "changedsourcepin":
				f.native.SourceSnapshot.ID = "other"
			case "started":
				f.native.Status = "IN_PROGRESS"
			}
			_, err = e.Apply(context.Background(), a, digest, auth)
			if mode == "ok" {
				if err != nil || !called || f.execute != 1 {
					t.Fatalf("apply err=%v called=%v execute=%d", err, called, f.execute)
				}
			} else if err == nil || f.execute != 0 {
				t.Fatalf("unsafe apply err=%v execute=%d", err, f.execute)
			}
		})
	}
}

func TestRollbackPlanRequiresExplicitNativeOriginalLink(t *testing.T) {
	for _, mode := range []string{"missing", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			original, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			f.native.Status = "COMPLETED"
			f.rollbackLinkMode = mode
			if _, err = e.RollbackPlan(context.Background(), original, testOrigin()); err == nil || f.execute != 0 {
				t.Fatalf("native rollback with %s original link accepted: err=%v execute=%d", mode, err, f.execute)
			}
		})
	}
}

func TestApplyRejectsRollbackWhoseNativeOriginalLinkChanged(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	original, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "COMPLETED"
	rollback, err := e.RollbackPlan(context.Background(), original, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Previous.ID = "other"
	if _, err = e.Apply(context.Background(), rollback, rollback.Review.Digest, func(context.Context, Artifact) error { return nil }); err == nil || f.execute != 0 {
		t.Fatalf("rollback with changed native original link executed: err=%v execute=%d", err, f.execute)
	}
}

func TestRollbackPlanBlocksMissingNativeTargetSnapshot(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	original, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "COMPLETED"
	f.omitTargetSnapshot = true
	rollback, err := e.RollbackPlan(context.Background(), original, testOrigin())
	if err == nil && len(rollback.Review.Issues) == 0 {
		t.Fatal("rollback with missing native target snapshot produced an executable review")
	}
	if f.execute != 0 {
		t.Fatal("rollback planning executed a native mutation")
	}
}

func TestApplyRejectsCurrentTargetDriftBeforeExecution(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.targetChanged = true
	authorized := false
	_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error {
		authorized = true
		return nil
	})
	if err == nil || f.execute != 0 {
		t.Fatalf("target drift was not blocked before execution: err=%v authorized=%v execute=%d", err, authorized, f.execute)
	}
}

func TestApplyFailsClosedWhenCurrentTargetInventoryIsUnavailableOrAmbiguous(t *testing.T) {
	for _, mode := range []string{"unavailable", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unavailable" {
				f.targetUnavailable = true
			} else {
				f.targetDuplicate = true
			}
			authorized := false
			_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error {
				authorized = true
				return nil
			})
			if err == nil || f.execute != 0 {
				t.Fatalf("unsafe target inventory mode %s passed: err=%v authorized=%v execute=%d", mode, err, authorized, f.execute)
			}
		})
	}
}

func TestApplyRejectsChangedReusedUnselectedDependency(t *testing.T) {
	f := fixture(t)
	f.reuseDependency = true
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.targetDependencyChanged = true
	_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil })
	if err == nil || f.execute != 0 {
		t.Fatalf("changed reused dependency was not blocked: err=%v execute=%d", err, f.execute)
	}
}

func TestApplyRejectsNewDestinationNameCollisionForCreate(t *testing.T) {
	f := fixture(t)
	source, target := testInventories(f.origin)
	request, err := NewRequest(source, target, "us", "us", []string{"flow"}, map[string]string{}, "create flow")
	if err != nil {
		t.Fatal(err)
	}
	f.req = request
	f.native.Mapping = map[string]string{}
	f.steps = []pingone.Step{{Action: "POST", URL: f.origin + "/v1/environments/target/flows", Source: pingone.Ref{ID: "flow"}, Payload: json.RawMessage(`{"name":"Flow"}`)}}
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), request, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Review.Issues) != 0 {
		t.Fatalf("create plan unexpectedly invalid: %v", a.Review.Issues)
	}
	f.targetCollision = true
	_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil })
	if err == nil || f.execute != 0 {
		t.Fatalf("new destination name collision was not blocked: err=%v execute=%d", err, f.execute)
	}
}

func TestRollbackDistinctPlanAndReviewedDelete(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "COMPLETED"
	rb, err := e.RollbackPlan(context.Background(), a, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	if rb.NativeID == a.NativeID || rb.OriginalNativeID != a.NativeID || rb.Kind != "rollback" || len(rb.Review.Issues) > 0 || f.execute != 0 {
		t.Fatalf("rollback %+v", rb)
	}
	if _, err = e.Apply(context.Background(), rb, rb.Review.Digest, func(context.Context, Artifact) error { return nil }); err != nil || f.execute != 1 {
		t.Fatalf("rollback execute %v", err)
	}
}

func TestRollbackPlanRejectsDifferentGitLabProjectBeforeNativeDraft(t *testing.T) {
	for _, mode := range []string{"server", "project"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			original, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			f.native.Status = "COMPLETED"
			current := testOrigin()
			if mode == "server" {
				current.ServerURL = "https://other-gitlab.example.com"
			} else {
				current.ProjectID = "789"
			}
			_, err = e.RollbackPlan(context.Background(), original, current)
			if err == nil || f.rollback != 0 {
				t.Fatalf("rollback crossed trusted GitLab %s: err=%v rollback drafts=%d", mode, err, f.rollback)
			}
		})
	}
}

func TestArtifactOriginAndTargetSnapshotAreDigestBound(t *testing.T) {
	for _, field := range []string{"origin", "targetsnapshot"} {
		t.Run(field, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			if field == "origin" {
				a.Origin.PipelineID = "999"
			} else {
				a.TargetSnapshotID = "otherpin"
			}
			if _, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil }); err == nil || f.execute != 0 {
				t.Fatalf("tampered %s executed", field)
			}
		})
	}
}
func TestNativeErrorsAndTerminalStatusNeverExecute(t *testing.T) {
	for _, mode := range []string{"errors", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "errors" {
				f.native.Errors = json.RawMessage(`[{"message":"server sensitive error"}]`)
			} else {
				f.native.Status = "COMPLETED"
			}
			if _, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil }); err == nil || f.execute != 0 {
				t.Fatal("unsafe native state executed")
			}
			fresh, err := e.Refresh(context.Background(), a)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(fresh)
			if strings.Contains(string(raw), "server sensitive error") {
				t.Fatal("raw native error persisted")
			}
		})
	}
}
func TestProductionRegionMismatchFailsBeforeNetwork(t *testing.T) {
	r := testRequest(t)
	r.Target.Region = "eu"
	e := &Engine{Client: &pingone.Client{BaseURL: "https://api.pingone.com"}}
	if _, err := e.Plan(context.Background(), r, testOrigin()); err == nil {
		t.Fatal("trusted origin mismatch accepted")
	}
}
func TestFailedPromotionCanProduceReviewedRollback(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "ERROR"
	f.native.Errors = json.RawMessage(`[{"message":"failed execution"}]`)
	if _, err = e.Status(context.Background(), a); err != nil {
		t.Fatalf("cannot reconcile failed execution: %v", err)
	}
	rb, err := e.RollbackPlan(context.Background(), a, testOrigin())
	if err != nil {
		t.Fatalf("cannot plan rollback after failed execution: %v", err)
	}
	if rb.NativeID == a.NativeID || f.execute != 0 {
		t.Fatal("rollback executed during planning")
	}
}

func TestRefreshPendingNativePlanReturnsBlockedReview(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "PREPARING"
	fresh, err := e.Refresh(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Review.Issues) == 0 || f.execute != 0 {
		t.Fatal("pending plan was not blocked")
	}
}

func TestOriginAllowsCleanEnterpriseSubpath(t *testing.T) {
	for _, raw := range []string{"https://gitlab.example.com/gitlab", "https://gitlab.example.com/company/gitlab/"} {
		origin := testOrigin()
		origin.ServerURL = raw
		if err := origin.Validate(); err != nil {
			t.Fatalf("clean enterprise subpath %q: %v", raw, err)
		}
	}
	for _, raw := range []string{"https://gitlab.example.com/a/../gitlab", "https://gitlab.example.com/a/./gitlab", "https://gitlab.example.com//gitlab", "https://gitlab.example.com/%67itlab", "https://user@gitlab.example.com/gitlab", "https://gitlab.example.com/gitlab?private=yes", "https://gitlab.example.com/gitlab#private"} {
		origin := testOrigin()
		origin.ServerURL = raw
		if err := origin.Validate(); err == nil {
			t.Fatalf("unsafe enterprise origin accepted: %q", raw)
		}
	}
}
func TestNativeWaitHonorsCanceledAndPendingContexts(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.wait(ctx, "source", "native"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v", err)
	}
	f.native.Status = "PREPARING"
	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := e.wait(ctx, "source", "native"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending wait error = %v", err)
	}
	if f.execute != 0 {
		t.Fatal("pending wait executed native plan")
	}
}
func TestApplyPendingNativePlanNeverAuthorizesOrExecutes(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "PREPARING"
	called := false
	if _, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { called = true; return nil }); err == nil || called || f.execute != 0 {
		t.Fatalf("pending apply: err=%v authorize=%v execute=%d", err, called, f.execute)
	}
}

// Regression for HANDOFF review item 1: a reviewed plan must not execute when the
// live destination has drifted from the pinned target snapshot for any resource the
// plan touches, and an unreadable live target must fail closed.
func TestApplyBlocksLiveTargetDriftForAffectedResources(t *testing.T) {
	edit := func(field string, value any) func([]pingone.Resource) []pingone.Resource {
		return func(in []pingone.Resource) []pingone.Resource {
			out := []pingone.Resource{}
			for _, r := range in {
				if r.ID == "dest" {
					cfg := map[string]any{}
					for k, v := range r.Configuration {
						cfg[k] = v
					}
					cfg[field] = value
					r.Configuration = cfg
				}
				out = append(out, r)
			}
			return out
		}
	}
	cases := map[string]struct {
		live    func([]pingone.Resource) []pingone.Resource
		fail    bool
		allowed bool
	}{
		"unchanged": {live: func(in []pingone.Resource) []pingone.Resource { return in }, allowed: true},
		"unrelated-added": {live: func(in []pingone.Resource) []pingone.Resource {
			return append(in, pingone.Resource{ID: "tpl", URL: "PLACEHOLDER/v1/environments/target/templates/tpl", Type: "templates", Configuration: map[string]any{"name": "New"}})
		}, allowed: true},
		"edited":      {live: edit("name", "Edited in console")},
		"secret-only": {live: edit("clientSecret", "rotated")},
		"deleted": {live: func(in []pingone.Resource) []pingone.Resource {
			out := []pingone.Resource{}
			for _, r := range in {
				if r.ID != "dest" {
					out = append(out, r)
				}
			}
			return out
		}},
		"unavailable": {fail: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			if c.live != nil {
				live := c.live
				f.liveTarget = func(in []pingone.Resource) []pingone.Resource {
					out := live(in)
					for i := range out {
						out[i].URL = strings.Replace(out[i].URL, "PLACEHOLDER", f.origin, 1)
					}
					return out
				}
			}
			f.liveTargetFail = c.fail
			_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil })
			if c.allowed {
				if err != nil || f.execute != 1 {
					t.Fatalf("expected execution: err=%v execute=%d", err, f.execute)
				}
				return
			}
			if err == nil || f.execute != 0 {
				t.Fatalf("drift not blocked: err=%v execute=%d", err, f.execute)
			}
		})
	}
}

func TestRollbackApplyBlocksLiveTargetDrift(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "COMPLETED"
	rb, err := e.RollbackPlan(context.Background(), a, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.liveTarget = func(in []pingone.Resource) []pingone.Resource {
		for i := range in {
			if in[i].ID == "dest" {
				in[i].Configuration["name"] = "Manual edit after rollback review"
			}
		}
		return in
	}
	if _, err = e.Apply(context.Background(), rb, rb.Review.Digest, func(context.Context, Artifact) error { return nil }); err == nil || f.execute != 0 {
		t.Fatalf("rollback drift not blocked: err=%v execute=%d", err, f.execute)
	}
}

func TestApplyBlocksConcurrentCreateIntoPlannedCollection(t *testing.T) {
	for name, c := range map[string]struct {
		typ     string
		allowed bool
	}{"same-collection": {"flows", false}, "other-collection": {"templates", true}} {
		t.Run(name, func(t *testing.T) {
			f := fixture(t)
			src, dst := testInventories(f.origin)
			req, err := NewRequest(src, dst, "us", "us", []string{"flow"}, nil, "create")
			if err != nil {
				t.Fatal(err)
			}
			f.native.Mapping = nil
			f.steps = []pingone.Step{{Action: "POST", URL: f.origin + "/v1/environments/target/flows", Source: pingone.Ref{ID: "flow"}, Payload: json.RawMessage(`{"name":"Flow"}`)}}
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), req, testOrigin())
			if err != nil || len(a.Review.Issues) > 0 {
				t.Fatalf("plan err=%v issues=%v", err, a.Review.Issues)
			}
			f.liveTarget = func(in []pingone.Resource) []pingone.Resource {
				return append(in, pingone.Resource{ID: "new", URL: f.origin + "/v1/environments/target/" + c.typ + "/new", Type: c.typ, Configuration: map[string]any{"name": "Flow"}})
			}
			_, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { return nil })
			if c.allowed != (err == nil && f.execute == 1) {
				t.Fatalf("allowed=%v err=%v execute=%d", c.allowed, err, f.execute)
			}
		})
	}
}
