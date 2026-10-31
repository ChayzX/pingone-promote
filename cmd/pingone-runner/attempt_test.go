package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func attemptFixture(t *testing.T) gitops.DraftAttempt {
	r, a := fixture(t)
	return gitops.DraftAttempt{Version: 1, Kind: "promotion", RequestID: r.ID, RequestDigest: strings.Repeat("d", 64), Origin: a.Origin, Source: r.Source, Target: r.Target, TargetSnapshotID: a.TargetSnapshotID, CreatedAt: a.CreatedAt}
}
func TestAttemptStoreExclusiveAndBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json.draft-attempt")
	s := draftAttemptStore{path: path}
	a := attemptFixture(t)
	var wg sync.WaitGroup
	wins := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); wins <- s.Begin(context.Background(), a) == nil }()
	}
	wg.Wait()
	close(wins)
	n := 0
	for won := range wins {
		if won {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("exclusive winners=%d", n)
	}
	a.NativeID = "native"
	if err := s.Checkpoint(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	b, err := readDraftAttempt(path)
	if err != nil || b.NativeID != "native" {
		t.Fatalf("identity lost: %+v %v", b, err)
	}
	a.RequestDigest = strings.Repeat("e", 64)
	if s.Checkpoint(context.Background(), a) == nil {
		t.Fatal("accepted changed binding")
	}
	raw, _ := os.ReadFile(path)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	for _, k := range []string{"request", "description", "configurations", "selected", "mappings"} {
		if _, ok := fields[k]; ok {
			t.Fatalf("payload metadata: %s", k)
		}
	}
}
func TestAttemptStoreRefusesMissingMalformedAndSymlink(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "attempt")
			switch mode {
			case "malformed":
				os.WriteFile(p, []byte(`{"version":1,"extra":true}`), 0600)
			case "symlink":
				target := p + "target"
				os.WriteFile(target, []byte("{}"), 0600)
				if err := os.Symlink(target, p); err != nil {
					t.Skip(err)
				}
			}
			a := attemptFixture(t)
			a.NativeID = "native"
			if (&draftAttemptStore{path: p}).Checkpoint(context.Background(), a) == nil {
				t.Fatal("checkpoint recreated or replaced invalid intent")
			}
		})
	}
}
func TestRunnerBlocksExistingAttemptBeforeClient(t *testing.T) {
	for _, command := range []string{"plan", "rollback-plan"} {
		t.Run(command, func(t *testing.T) {
			r, a := fixture(t)
			d := t.TempDir()
			rp := filepath.Join(d, "requests", r.ID, "request.json")
			ap := filepath.Join(d, "original.json")
			out := filepath.Join(d, "plan.json")
			putJSON(t, rp, r)
			putJSON(t, ap, a)
			os.WriteFile(out+".draft-attempt", []byte("broken"), 0600)
			called := false
			dd := deps(&fakeGate{origin: a.Origin})
			dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { called = true; return nil, nil }
			dd.engine = func(*pingone.Client) runnerEngine { return &fakeEngine{artifact: a} }
			args := []string{command, "--out", out}
			if command == "plan" {
				args = append(args, "--request", rp)
			} else {
				args = append(args, "--artifact", ap, "--original-job-id", "41")
			}
			var stdout, stderr strings.Builder
			if err := runWith(args, &stdout, &stderr, dd); err == nil || called {
				t.Fatalf("existing malformed attempt allowed client: called=%v err=%v", called, err)
			}
		})
	}
}
func TestRunnerRejectsSidecarOutputAlias(t *testing.T) {
	r, a := fixture(t)
	d := t.TempDir()
	rp := filepath.Join(d, "requests", r.ID, "request.json")
	putJSON(t, rp, r)
	out := filepath.Join(d, "plan.json")
	called := false
	dd := deps(&fakeGate{origin: a.Origin})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) { called = true; return nil, nil }
	dd.engine = func(*pingone.Client) runnerEngine { return &fakeEngine{artifact: a} }
	var stdout, stderr strings.Builder
	if err := runWith([]string{"plan", "--request", rp, "--out", out, "--digest-out", out + ".draft-attempt"}, &stdout, &stderr, dd); err == nil || called {
		t.Fatalf("sidecar alias accepted: called=%v err=%v", called, err)
	}
}

// Synthetic transport exercises real runner/engine/store effects, without service calls.
type draftTransport struct {
	posts    atomic.Int32
	onPoll   func()
	failPost bool
}

func (tr *draftTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	env, pin := "source", "srcpin"
	if strings.Contains(r.URL.Path, "/target") {
		env, pin = "target", "dstpin"
	}
	var value any
	switch {
	case strings.Contains(r.URL.Path, "/promotions"):
		if r.Method == "POST" {
			tr.posts.Add(1)
			if tr.failPost {
				return nil, errors.New("response lost")
			}
		} else if tr.onPoll != nil {
			tr.onPoll()
		}
		if strings.Contains(r.URL.Path, "promotionSteps") {
			value = map[string]any{"_embedded": map[string]any{"promotionSteps": []any{}}}
		} else {
			value = pingone.Promotion{ID: "native-1", Status: "READY", Source: pingone.Ref{ID: "source"}, Target: pingone.Ref{ID: "target"}, SourceSnapshot: pingone.Ref{ID: "srcpin"}, TargetSnapshot: pingone.Ref{ID: "dstpin"}, Mapping: map[string]string{}}
		}
	case strings.Contains(r.URL.Path, "embeddedSnapshots"):
		items := []any{}
		if env == "source" {
			items = append(items, map[string]any{"resource": map[string]string{"id": "flow"}, "resourceUrl": "https://127.0.0.1/v1/environments/source/flows/flow", "configuration": map[string]any{"name": "flow"}})
		}
		value = map[string]any{"_embedded": map[string]any{"referencedResources": items}}
	case strings.Contains(r.URL.Path, "/snapshots"):
		value = map[string]any{"snapshotId": pin, "resource": map[string]string{"id": env}, "status": "COMPLETE"}
	default:
		value = map[string]any{"id": env, "organization": map[string]string{"id": "org"}}
	}
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
}
func TestConcreteRunnerOutputFailureRetainsAttemptAndBlocksRestart(t *testing.T) {
	r, a := fixture(t)
	d := t.TempDir()
	rp := filepath.Join(d, "requests", r.ID, "request.json")
	out := filepath.Join(d, "plan.json")
	putJSON(t, rp, r)
	tr := &draftTransport{}
	tr.onPoll = func() {
		if err := os.Mkdir(out, 0700); err != nil && !os.IsExist(err) {
			t.Error(err)
		}
	}
	dd := deps(&fakeGate{origin: a.Origin})
	dd.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
		return &pingone.Client{BaseURL: "https://127.0.0.1", HTTP: &http.Client{Transport: tr}, AccessToken: "test"}, nil
	}
	var stdout, stderr strings.Builder
	args := []string{"plan", "--request", rp, "--out", out}
	if err := runWith(args, &stdout, &stderr, dd); err == nil {
		t.Fatal("expected final output fault")
	}
	saved, err := readDraftAttempt(out + ".draft-attempt")
	if err != nil || saved.NativeID != "native-1" || tr.posts.Load() != 1 {
		t.Fatalf("lost checkpoint %+v posts=%d err=%v", saved, tr.posts.Load(), err)
	}
	os.Remove(out)
	if err := runWith(args, &stdout, &stderr, dd); err == nil || tr.posts.Load() != 1 {
		t.Fatalf("restart dispatched again: %v posts=%d", err, tr.posts.Load())
	}
}
func TestConcreteEnginesConcurrentAttemptAllowsOneDraft(t *testing.T) {
	r, a := fixture(t)
	out := filepath.Join(t.TempDir(), "plan.json")
	tr := &draftTransport{}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &pingone.Client{BaseURL: "https://127.0.0.1", HTTP: &http.Client{Transport: tr}, AccessToken: "test"}
			e := bindDraftAttempts(&gitops.Engine{Client: client}, out)
			e.Plan(context.Background(), r, a.Origin)
		}()
	}
	wg.Wait()
	if tr.posts.Load() != 1 {
		t.Fatalf("draft posts=%d", tr.posts.Load())
	}
}
func TestConcretePostFailureRetainsIntent(t *testing.T) {
	r, a := fixture(t)
	out := filepath.Join(t.TempDir(), "plan.json")
	tr := &draftTransport{failPost: true}
	client := &pingone.Client{BaseURL: "https://127.0.0.1", HTTP: &http.Client{Transport: tr}, AccessToken: "test"}
	e := bindDraftAttempts(&gitops.Engine{Client: client}, out)
	_, err := e.Plan(context.Background(), r, a.Origin)
	saved, readErr := readDraftAttempt(out + ".draft-attempt")
	if err == nil || readErr != nil || saved.NativeID != "" {
		t.Fatalf("missing uncertain intent: %+v %v %v", saved, err, readErr)
	}
	e.Plan(context.Background(), r, a.Origin)
	if tr.posts.Load() != 1 {
		t.Fatalf("repeat posts=%d", tr.posts.Load())
	}
}
