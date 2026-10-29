package cigate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
	"strings"
	"testing"
)

func fixtureRequest(t *testing.T) gitops.Request {
	t.Helper()
	src := pingone.Inventory{EnvironmentID: "src", OrganizationID: "org", SnapshotID: "s1", Resources: []pingone.Resource{{ID: "flow", URL: "https://api.pingone.com/v1/environments/src/flows/flow", Type: "flows", Configuration: map[string]any{"name": "Flow"}}}}
	dst := pingone.Inventory{EnvironmentID: "prod", OrganizationID: "org", SnapshotID: "t1", Resources: []pingone.Resource{{ID: "target", URL: "https://api.pingone.com/v1/environments/prod/flows/target", Type: "flows"}}}
	r, e := gitops.NewRequest(src, dst, "us", "us", []string{"flow"}, map[string]string{"flow": "target"}, "test")
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestFromEnvRejectsMissingAndSpoofedCIContext(t *testing.T) {
	base := map[string]string{"GITLAB_CI": "true", "CI_SERVER_URL": "https://gitlab.example.test", "PROMOTION_GITLAB_URL": "https://gitlab.example.test", "CI_PROJECT_ID": "7", "PROMOTION_GITLAB_PROJECT_ID": "7", "CI_PIPELINE_ID": "9", "CI_JOB_ID": "11", "CI_COMMIT_SHA": strings.Repeat("a", 40), "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main", "CI_COMMIT_REF_PROTECTED": "true", "CI_PIPELINE_SOURCE": "push", "CI_JOB_TOKEN": "job-token", "GITLAB_READ_API_TOKEN": "read-token", "PINGONE_TARGET_ENVIRONMENT_ID": "prod", "PINGONE_TARGET_REGION": "us"}
	for _, key := range []string{"GITLAB_CI", "CI_COMMIT_REF_PROTECTED", "CI_JOB_TOKEN", "GITLAB_READ_API_TOKEN", "PINGONE_TARGET_ENVIRONMENT_ID", "PROMOTION_GITLAB_URL"} {
		t.Run(key, func(t *testing.T) {
			_, err := FromEnv(func(k string) string {
				if k == key {
					return ""
				}
				return base[k]
			})
			if err == nil {
				t.Fatal("missing trusted context accepted")
			}
		})
	}
	if _, err := FromEnv(func(k string) string { return base[k] }); err != nil {
		t.Fatal(err)
	}
}
func TestLiveGateRequiresMatchingProtectedApprovedDeployment(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong job", "unprotected branch", "pending approval", "no protection", "wrong pipeline", "wrong target", "no deployment", "retried job on later page", "external redirect"} {
		t.Run(scenario, func(t *testing.T) {
			req := fixtureRequest(t)
			sha := strings.Repeat("a", 40)
			var ts *httptest.Server
			ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v4/job" {
					if r.Header.Get("JOB-TOKEN") != "job-token" || r.Header.Get("PRIVATE-TOKEN") != "" {
						t.Error("job identity used wrong credential")
					}
				} else if r.Header.Get("PRIVATE-TOKEN") != "read-token" {
					t.Error("read API used wrong credential")
				}
				send := func(v any) { json.NewEncoder(w).Encode(v) }
				switch r.URL.Path {
				case "/api/v4/job":
					id := 11
					if scenario == "wrong job" {
						id = 12
					}
					send(map[string]any{"id": id, "status": "running", "name": "deploy", "ref": "main", "tag": false, "commit": map[string]string{"id": sha}, "pipeline": map[string]any{"id": 9, "project_id": 7, "sha": sha, "ref": "main", "source": "push"}})
				case "/api/v4/projects/7":
					send(map[string]any{"id": 7, "visibility": "private", "default_branch": "main"})
				case "/api/v4/projects/7/repository/branches/main":
					if scenario == "external redirect" {
						http.Redirect(w, r, "https://outside.invalid/token", 302)
						return
					}
					send(map[string]any{"name": "main", "protected": scenario != "unprotected branch"})
				case "/api/v4/projects/7/protected_environments/production":
					n := 1
					if scenario == "no protection" {
						n = 0
					}
					send(map[string]any{"name": "production", "required_approval_count": n, "approval_rules": []any{map[string]int{"required_approvals": n}}})
				case "/api/v4/projects/7/pipelines/9/jobs":
					if scenario == "retried job on later page" && r.URL.Query().Get("page") == "1" {
						w.Header().Set("X-Next-Page", "2")
						send([]any{map[string]any{"id": 11, "name": "deploy"}})
						return
					}
					id := 11
					if scenario == "retried job on later page" {
						id = 10
					}
					send([]any{map[string]any{"id": id, "name": "deploy"}})
				case "/api/v4/projects/7/deployments":
					if scenario == "no deployment" {
						send([]any{})
						return
					}
					send([]any{map[string]any{"id": 17, "deployable": map[string]int{"id": 11}}})
				case "/api/v4/projects/7/deployments/17":
					status := "approved"
					if scenario == "pending approval" {
						status = "rejected"
					}
					send(map[string]any{"id": 17, "status": "running", "sha": sha, "ref": "main", "environment": map[string]any{"id": 3, "name": "production"}, "deployable": map[string]any{"id": 11, "pipeline": map[string]any{"id": 9, "sha": sha}}, "approval_summary": map[string]any{"rules": []any{map[string]any{"required_approvals": 1, "deployment_approvals": []any{map[string]any{"status": status, "user": map[string]int{"id": 5}}}}}}})
				default:
					t.Errorf("unexpected live gate API %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			c := Config{BaseURL: ts.URL, ExpectedURL: ts.URL, ProjectID: "7", ExpectedProjectID: "7", PipelineID: "9", JobID: "11", CommitSHA: sha, Branch: "main", DefaultBranch: "main", Environment: "production", TargetEnvironmentID: "prod", TargetRegion: "us", JobToken: "job-token", ReadToken: "read-token", ProtectedRef: true, PipelineSource: "push"}
			if scenario == "wrong target" {
				c.TargetEnvironmentID = "other"
			}
			v := Verifier{Config: c, HTTP: ts.Client()}
			a := gitops.Artifact{Version: 1, Kind: "promotion", NativeID: "native", CreatedAt: req.CreatedAt, Request: req, Origin: v.Origin(), TargetSnapshotID: "t1", Review: promotion.Review{Digest: strings.Repeat("f", 64)}}
			if scenario == "wrong pipeline" {
				a.Origin.PipelineID = "10"
			}
			err := v.Authorize(context.Background(), a)
			if scenario == "valid" && err != nil {
				t.Fatal(err)
			}
			if scenario != "valid" && err == nil {
				t.Fatal("unapproved CI execution authorized")
			}
		})
	}
}

// A runner environment variable can be forged by a sufficiently privileged CI
// configuration. The live Job API source must independently match it.
func TestVerifyRequiresPipelineSourceBoundToGitLabJobAPI(t *testing.T) {
	for _, tc := range []struct {
		name, configuredSource, pipelineSource, topLevelSource string
		wantErr, omitPipelineSource                            bool
	}{
		{name: "nested push source without top-level source", configuredSource: "push", pipelineSource: "push"},
		{name: "nested web source without top-level source", configuredSource: "web", pipelineSource: "web"},
		{name: "nested trigger rejects top-level web", configuredSource: "web", pipelineSource: "trigger", topLevelSource: "web", wantErr: true},
		{name: "missing nested source rejects top-level web", configuredSource: "web", topLevelSource: "web", omitPipelineSource: true, wantErr: true},
		{name: "nested source mismatch", configuredSource: "push", pipelineSource: "web", wantErr: true},
		{name: "nested api source rejected", configuredSource: "push", pipelineSource: "api", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v4/job" && r.Header.Get("JOB-TOKEN") != "job-token" {
					t.Errorf("job endpoint did not use the job token")
				}
				if r.URL.Path != "/api/v4/job" && r.Header.Get("PRIVATE-TOKEN") != "read-token" {
					t.Errorf("read endpoint did not use the read token")
				}
				switch r.URL.Path {
				case "/api/v4/job":
					pipeline := map[string]any{"id": 9, "project_id": 7, "sha": sha, "ref": "main"}
					if !tc.omitPipelineSource {
						pipeline["source"] = tc.pipelineSource
					}
					payload := map[string]any{"id": 11, "status": "running", "name": "deploy", "ref": "main", "tag": false, "commit": map[string]string{"id": sha}, "pipeline": pipeline}
					if tc.topLevelSource != "" {
						payload["source"] = tc.topLevelSource
					}
					json.NewEncoder(w).Encode(payload)
				case "/api/v4/projects/7":
					json.NewEncoder(w).Encode(map[string]any{"id": 7, "visibility": "private", "default_branch": "main"})
				case "/api/v4/projects/7/repository/branches/main":
					json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": true})
				default:
					t.Errorf("unexpected API request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			c := Config{BaseURL: ts.URL, ExpectedURL: ts.URL, ProjectID: "7", ExpectedProjectID: "7", PipelineID: "9", JobID: "11", CommitSHA: sha, Branch: "main", DefaultBranch: "main", Environment: "production", TargetEnvironmentID: "prod", TargetRegion: "us", JobToken: "job-token", ReadToken: "read-token", ProtectedRef: true, PipelineSource: tc.configuredSource}
			v := Verifier{Config: c, HTTP: ts.Client()}
			err := v.VerifyPlan(context.Background(), fixtureRequest(t))
			if tc.wantErr && err == nil {
				t.Fatal("job API source absent or different from CI_PIPELINE_SOURCE was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("matching pipeline source rejected: %v", err)
			}
		})
	}
}

// A rollback artifact from a non-plan job or a different source revision must
// not be accepted just because a caller supplied an artifact download URL.
func TestVerifyOriginalPlanRequiresBoundSuccessfulDefaultBranchPlanJob(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong job name", "failed source job", "wrong source pipeline", "wrong source project", "wrong source commit", "wrong source ref", "tag ref", "missing tag metadata", "foreign artifact project", "foreign artifact server", "wrong job id"} {
		t.Run(scenario, func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			var ts *httptest.Server
			ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/v4/projects/7/jobs/") {
					t.Errorf("unexpected GitLab API request %s", r.URL)
					http.NotFound(w, r)
					return
				}
				if r.Header.Get("PRIVATE-TOKEN") != "read-token" {
					t.Errorf("source job lookup did not use the read token")
				}
				name, status, ref, pipelineRef, tag, pipelineID, pipelineProjectID, commit := "plan_promotion", "success", "main", "main", false, 9, 7, sha
				switch scenario {
				case "wrong job name":
					name = "apply_promotion"
				case "failed source job":
					status = "failed"
				case "wrong source pipeline":
					pipelineID = 10
				case "wrong source project":
					pipelineProjectID = 8
				case "wrong source commit":
					commit = strings.Repeat("b", 40)
				case "wrong source ref":
					ref, pipelineRef = "feature/test", "feature/test"
				case "tag ref":
					tag = true
				}
				payload := map[string]any{
					"id": 41, "name": name, "status": status, "ref": ref, "tag": tag,
					"commit":   map[string]string{"id": commit},
					"pipeline": map[string]any{"id": pipelineID, "project_id": pipelineProjectID, "sha": sha, "ref": pipelineRef, "source": "push"},
				}
				if scenario == "missing tag metadata" {
					delete(payload, "tag")
				}
				json.NewEncoder(w).Encode(payload)
			}))
			defer ts.Close()
			c := Config{BaseURL: ts.URL, ExpectedURL: ts.URL, ProjectID: "7", ExpectedProjectID: "7", PipelineID: "12", JobID: "51", CommitSHA: strings.Repeat("c", 40), Branch: "main", DefaultBranch: "main", Environment: "production", TargetEnvironmentID: "prod", TargetRegion: "us", JobToken: "job-token", ReadToken: "read-token", ProtectedRef: true, PipelineSource: "web"}
			v := Verifier{Config: c, HTTP: ts.Client()}
			a := gitops.Artifact{Version: 1, Kind: "promotion", NativeID: "native", CreatedAt: "2026-01-01T00:00:00Z", Request: fixtureRequest(t), Origin: gitops.Origin{ServerURL: ts.URL, ProjectID: "7", PipelineID: "9", CommitSHA: sha}, TargetSnapshotID: "target", Review: promotion.Review{Digest: strings.Repeat("f", 64)}}
			jobID := "41"
			if scenario == "wrong job id" {
				jobID = "42"
			}
			if scenario == "foreign artifact project" {
				a.Origin.ProjectID = "8"
			}
			if scenario == "foreign artifact server" {
				a.Origin.ServerURL = "https://other-gitlab.example"
			}
			err := v.VerifyOriginalPlan(context.Background(), a, jobID)
			if scenario == "valid" && err != nil {
				t.Fatalf("valid original plan rejected: %v", err)
			}
			if scenario != "valid" && err == nil {
				t.Fatal("unbound original plan was accepted")
			}
		})
	}
}
