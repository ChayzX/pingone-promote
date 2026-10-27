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
	page2                     bool
	omitTarget                bool
	seenBody                  map[string]any
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
		json.NewEncoder(w).Encode(p)
		return
	}
	env := "source"
	pin := "sourcepin"
	if strings.HasPrefix(path, "/v1/environments/target") {
		env = "target"
		pin = "targetpin"
	}
	if strings.HasSuffix(path, "/embeddedSnapshots") {
		src, dst := testInventories(f.origin)
		inv := src
		if env == "target" {
			inv = dst
		}
		items := []any{}
		for _, res := range inv.Resources {
			if f.mutatedSource && res.ID == "flow" {
				res.Configuration["name"] = "Changed"
			}
			items = append(items, map[string]any{"resource": map[string]string{"id": res.ID}, "resourceUrl": res.URL, "configuration": res.Configuration})
		}
		json.NewEncoder(w).Encode(map[string]any{"_embedded": map[string]any{"referencedResources": items}})
		return
	}
	if strings.Contains(path, "/snapshots") {
		if r.Method == "POST" && env != "target" {
			http.Error(w, "source pin bypass", 400)
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
	for _, mode := range []string{"noauth", "wrongdigest", "changedstep", "changedmapping", "changedsourcepin", "started", "denied", "ok"} {
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
