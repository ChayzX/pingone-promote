package gitops

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestLifecycleUnknownStatusKeepsMappingChecks(t *testing.T) {
	for _, status := range []string{"completed", "Error", "in_progress", "rolled_back", "", "FUTURE", "VALIDATION_FAILED"} {
		t.Run(status, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			f.native.Status = status
			f.native.Mapping = map[string]string{"flow": "elsewhere"}
			if _, err = e.Status(context.Background(), a); err == nil {
				t.Fatal("unknown relaxed mapping check")
			}
			auth := 0
			if _, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { auth++; return nil }); err == nil || auth != 0 || f.execute != 0 {
				t.Fatalf("unknown dispatched: err=%v auth=%d execute=%d", err, auth, f.execute)
			}
		})
	}
}
func TestLifecycleRollbackEligibility(t *testing.T) {
	for _, status := range []string{"COMPLETED", "ERROR", "ROLLED_BACK", "READY", "completed", "FUTURE"} {
		t.Run(status, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			f.native.Status = status
			_, err = e.RollbackPlan(context.Background(), a, testOrigin())
			allowed := status == "COMPLETED" || status == "ERROR"
			if allowed && (err != nil || f.rollback != 1) || !allowed && (err == nil || f.rollback != 0) {
				t.Fatalf("err=%v rollback=%d", err, f.rollback)
			}
		})
	}
}

func TestLifecyclePreparationSkipsSteps(t *testing.T) {
	for _, status := range []string{"NEW", "PREPARING", "ADDITIONAL_SNAPSHOTS_REQUESTED", "ROLLBACK_NEW", "ROLLBACK_PREPARING"} {
		t.Run(status, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			f.native.Status = status
			calls := 0
			original := f.client.HTTP.Transport
			f.client.HTTP.Transport = lifecycleTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/promotionSteps") {
					calls++
				}
				return original.RoundTrip(r)
			})
			got, err := e.Refresh(context.Background(), a)
			if err != nil || calls != 0 || len(got.Review.Issues) == 0 {
				t.Fatalf("refresh err=%v steps=%d issues=%v", err, calls, got.Review.Issues)
			}
			auth := 0
			if _, err = e.Apply(context.Background(), a, a.Review.Digest, func(context.Context, Artifact) error { auth++; return nil }); err == nil || auth != 0 || f.execute != 0 {
				t.Fatalf("preparation dispatched: err=%v auth=%d execute=%d", err, auth, f.execute)
			}
		})
	}
}

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
