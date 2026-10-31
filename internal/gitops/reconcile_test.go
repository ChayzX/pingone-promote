package gitops

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestReconcileLostCheckpointReadOnly(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	store := &faultAttempts{checkpointErr: errors.New("disk fault")}
	e.DraftAttempts = store
	_, err := e.Plan(context.Background(), f.req, testOrigin())
	if err == nil {
		t.Fatal("expected checkpoint fault")
	}
	attempt := store.checkpointed[0]
	f.create = 0
	f.client.HTTP.Transport = readOnlyTransport{f.client.HTTP.Transport, t}
	reconciler, ok := any(e).(interface {
		ReconcileDraft(context.Context, DraftAttempt, Request, Origin) (Artifact, error)
	})
	if !ok {
		t.Fatal("missing read-only checkpoint recovery")
	}
	a, err := reconciler.ReconcileDraft(context.Background(), attempt, f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	if a.Origin != attempt.Origin || a.CreatedAt != attempt.CreatedAt || a.Review.Digest == "" {
		t.Fatal("lost provenance")
	}
	if f.create+f.rollback+f.execute != 0 {
		t.Fatal("recovery mutated native service")
	}
}
func TestReconcileRejectsChangedEvidenceWithoutMutation(t *testing.T) {
	for _, mode := range []string{"request", "origin", "empty", "native", "source", "target", "sourcepin", "rollback", "rollback-pin"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			f.client.HTTP.Transport = readOnlyTransport{f.client.HTTP.Transport, t}
			a := newDraftAttempt(f.req, testOrigin(), "promotion", "", "targetpin")
			a.NativeID = "native"
			r := f.req
			o := testOrigin()
			switch mode {
			case "request":
				a.RequestDigest = string(make([]byte, 64))
			case "origin":
				o.ProjectID = "99"
			case "empty":
				a.NativeID = ""
			case "native":
				f.native.ID = "other"
			case "source":
				f.native.Source.ID = "other"
			case "target":
				f.native.Target.ID = "other"
			case "sourcepin":
				f.native.SourceSnapshot.ID = "other"
			case "rollback":
				a.Kind = "rollback"
				a.OriginalNativeID = "original"
			case "rollback-pin":
				a.Kind = "rollback"
				a.OriginalNativeID = "original"
				f.native.Previous.ID = "original"
				f.omitTargetSnapshot = true
			}
			_, err := (&Engine{Client: f.client}).ReconcileDraft(context.Background(), a, r, o)
			if err == nil {
				t.Fatal("changed evidence accepted")
			}
			if f.create+f.rollback+f.execute != 0 {
				t.Fatal("mutation")
			}
		})
	}
}

type readOnlyTransport struct {
	base http.RoundTripper
	t    *testing.T
}

func (tr readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != "GET" {
		tr.t.Errorf("reconciliation attempted %s %s", r.Method, r.URL.Path)
		return nil, errors.New("mutation prohibited")
	}
	return tr.base.RoundTrip(r)
}
