package cigate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"pingone-promote/internal/gitops"
	"strings"
	"testing"
)

func TestDraftAttemptRetainedEvidence(t *testing.T) {
	for _, mode := range []string{"failed", "success", "canceled", "changed", "duplicate", "missing", "redirect", "wrong-job", "running", "wrong-source", "wrong-project", "wrong-origin"} {
		t.Run(mode, func(t *testing.T) {
			var a gitops.DraftAttempt
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Fatal("mutation")
				}
				if r.Header.Get("PRIVATE-TOKEN") != "read" {
					t.Error("credential boundary")
				}
				if strings.Contains(r.URL.Path, "/artifacts/") {
					if !strings.HasSuffix(r.URL.Path, "/plan.json.draft-attempt") {
						t.Error("unexpected path")
					}
					switch mode {
					case "missing":
						http.NotFound(w, r)
						return
					case "redirect":
						http.Redirect(w, r, "https://outside.invalid", 302)
						return
					case "duplicate":
						w.Write([]byte(`{"version":1,"version":1}`))
						return
					case "changed":
						a.NativeID = "other"
					}
					json.NewEncoder(w).Encode(a)
					return
				}
				status := mode
				if status != "success" && status != "canceled" && status != "running" {
					status = "failed"
				}
				name := "plan_promotion"
				if mode == "wrong-job" {
					name = "apply_promotion"
				}
				source := "push"
				if mode == "wrong-source" {
					source = "schedule"
				}
				project := 7
				if mode == "wrong-project" {
					project = 8
				}
				sha := strings.Repeat("a", 40)
				if mode == "wrong-origin" {
					sha = strings.Repeat("b", 40)
				}
				json.NewEncoder(w).Encode(map[string]any{"id": 41, "name": name, "status": status, "tag": false, "ref": "main", "commit": map[string]string{"id": sha}, "pipeline": map[string]any{"id": 9, "project_id": project, "sha": sha, "ref": "main", "source": source}})
			}))
			defer ts.Close()
			c := Config{BaseURL: ts.URL, ExpectedURL: ts.URL, ProjectID: "7", ExpectedProjectID: "7", PipelineID: "12", JobID: "51", CommitSHA: strings.Repeat("c", 40), Branch: "main", DefaultBranch: "main", Environment: "production", TargetEnvironmentID: "prod", TargetRegion: "us", JobToken: "job", ReadToken: "read", ProtectedRef: true, PipelineSource: "web"}
			r := fixtureRequest(t)
			a = gitops.DraftAttempt{Version: 1, Kind: "promotion", RequestID: r.ID, RequestDigest: strings.Repeat("d", 64), Origin: gitops.Origin{ServerURL: ts.URL, ProjectID: "7", PipelineID: "9", CommitSHA: strings.Repeat("a", 40)}, NativeID: "native", Source: r.Source, Target: r.Target, TargetSnapshotID: "t1", CreatedAt: "2026-01-01T00:00:00Z"}
			err := (&Verifier{Config: c, HTTP: ts.Client()}).VerifyDraftAttempt(context.Background(), a, "41")
			valid := mode == "failed" || mode == "success" || mode == "canceled"
			if (err == nil) != valid {
				t.Fatalf("err=%v valid=%v", err, valid)
			}
		})
	}
}
