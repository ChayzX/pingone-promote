package gitops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"pingone-promote/internal/pingone"
	"strings"
	"testing"
)

type faultAttempts struct {
	beginErr, checkpointErr error
	begun, checkpointed     []DraftAttempt
}

func (s *faultAttempts) Begin(_ context.Context, a DraftAttempt) error {
	s.begun = append(s.begun, a)
	return s.beginErr
}
func (s *faultAttempts) Checkpoint(_ context.Context, a DraftAttempt) error {
	s.checkpointed = append(s.checkpointed, a)
	return s.checkpointErr
}
func TestDraftAttemptFaultBoundaries(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "begin", true: "checkpoint"}[checkpoint], func(t *testing.T) {
			f := fixture(t)
			s := &faultAttempts{}
			if checkpoint {
				s.checkpointErr = errors.New("disk fault")
			} else {
				s.beginErr = errors.New("disk fault")
			}
			e := &Engine{Client: f.client, DraftAttempts: s}
			a, err := e.Plan(context.Background(), f.req, testOrigin())
			want := 0
			if checkpoint {
				want = 1
			}
			if err == nil || f.create != want {
				t.Fatalf("err=%v posts=%d want=%d", err, f.create, want)
			}
			if checkpoint && (a.NativeID != "native" || len(s.checkpointed) != 1) {
				t.Fatalf("lost identity: %+v", a)
			}
		})
	}
}

// Removing checkpoint-before-poll must fail this assertion on the first native GET.
type attemptTransport struct {
	base  http.RoundTripper
	store *faultAttempts
	polls int
	t     *testing.T
}

func (tr *attemptTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "GET" && strings.Contains(r.URL.Path, "/promotions/") {
		tr.polls++
		if len(tr.store.checkpointed) == 0 {
			tr.t.Error("polled before identity checkpoint")
		}
	}
	return tr.base.RoundTrip(r)
}
func TestDraftCheckpointPrecedesPollAndFailureStopsPoll(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := fixture(t)
			s := &faultAttempts{}
			if fail {
				s.checkpointErr = errors.New("disk")
			}
			tr := &attemptTransport{base: f.client.HTTP.Transport, store: s, t: t}
			f.client.HTTP.Transport = tr
			e := &Engine{Client: f.client, DraftAttempts: s}
			_, err := e.Plan(context.Background(), f.req, testOrigin())
			if fail && (err == nil || tr.polls != 0) {
				t.Fatalf("checkpoint failure polled: %d %v", tr.polls, err)
			}
			if !fail && (err != nil || tr.polls == 0) {
				t.Fatalf("no successful poll: %d %v", tr.polls, err)
			}
		})
	}
}
func TestDraftValidationPrecedesIntent(t *testing.T) {
	f := fixture(t)
	s := &faultAttempts{}
	f.req.Source.ID = "../unsafe"
	_, err := (&Engine{Client: f.client, DraftAttempts: s}).Plan(context.Background(), f.req, testOrigin())
	if err == nil || len(s.begun) != 0 || f.create != 0 {
		t.Fatal("invalid request reached intent or dispatch")
	}
}
func TestRollbackAttemptFaultAndBadLink(t *testing.T) {
	for _, mode := range []string{"begin", "checkpoint", "wrong-link", "post-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			e := &Engine{Client: f.client}
			original, err := e.Plan(context.Background(), f.req, testOrigin())
			if err != nil {
				t.Fatal(err)
			}
			f.native.Status = "COMPLETED"
			s := &faultAttempts{}
			e.DraftAttempts = s
			switch mode {
			case "begin":
				s.beginErr = errors.New("disk")
			case "checkpoint":
				s.checkpointErr = errors.New("disk")
			case "wrong-link":
				f.rollbackLinkMode = "wrong"
			case "post-failure":
				f.rollbackRequestFails = true
			}
			a, err := e.RollbackPlan(context.Background(), original, testOrigin())
			if err == nil || len(s.begun) != 1 {
				t.Fatalf("err=%v intent=%d", err, len(s.begun))
			}
			if mode == "begin" && f.rollback != 0 {
				t.Fatal("dispatched despite intent failure")
			}
			if mode == "checkpoint" && (a.NativeID != "rollback" || len(s.checkpointed) != 1) {
				t.Fatal("lost accepted rollback identity")
			}
			if (mode == "wrong-link" || mode == "post-failure") && len(s.checkpointed) != 0 {
				t.Fatal("trusted invalid response")
			}
		})
	}
}

func TestRollbackCheckpointRejectsMismatchedNativeEnvironment(t *testing.T) {
	f := fixture(t)
	e := &Engine{Client: f.client}
	a, err := e.Plan(context.Background(), f.req, testOrigin())
	if err != nil {
		t.Fatal(err)
	}
	f.native.Status = "COMPLETED"
	s := &faultAttempts{}
	e.DraftAttempts = s
	// The transport changes only the accepted rollback response, after original status validation.
	f.client.HTTP.Transport = rollbackIdentityTransport{base: f.client.HTTP.Transport}
	_, err = e.RollbackPlan(context.Background(), a, testOrigin())
	if err == nil || len(s.checkpointed) != 0 {
		t.Fatalf("trusted mismatched rollback: err=%v checkpoints=%d", err, len(s.checkpointed))
	}
}

type rollbackIdentityTransport struct{ base http.RoundTripper }

func (tr rollbackIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := tr.base.RoundTrip(r)
	if err == nil && r.Method == "POST" && r.Header.Get("Content-Type") == pingone.RollbackType {
		var p pingone.Promotion
		json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		p.Target.ID = "other"
		b, _ := json.Marshal(p)
		resp.Body = io.NopCloser(bytes.NewReader(b))
	}
	return resp, err
}
