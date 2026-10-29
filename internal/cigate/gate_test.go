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
					send(map[string]any{"id": id, "status": "running", "name": "deploy", "ref": "main", "tag": false, "commit": map[string]string{"id": sha}, "pipeline": map[string]any{"id": 9, "project_id": 7, "sha": sha, "ref": "main"}})
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

// build-request calls VerifyTarget before any snapshot: it must apply the same
// protected-target and live job checks that VerifyPlan applies to a request.
func TestVerifyTargetChecksTargetPolicyAndLiveJob(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, scenario := range []string{"valid", "wrong target", "wrong region", "wrong job", "unprotected branch"} {
		t.Run(scenario, func(t *testing.T) {
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				send := func(v any) { json.NewEncoder(w).Encode(v) }
				switch r.URL.Path {
				case "/api/v4/job":
					id := 11
					if scenario == "wrong job" {
						id = 12
					}
					send(map[string]any{"id": id, "status": "running", "name": "plan_promotion", "ref": "main", "tag": false, "commit": map[string]string{"id": sha}, "pipeline": map[string]any{"id": 9, "project_id": 7, "sha": sha, "ref": "main"}})
				case "/api/v4/projects/7":
					send(map[string]any{"id": 7, "visibility": "private", "default_branch": "main"})
				case "/api/v4/projects/7/repository/branches/main":
					send(map[string]any{"name": "main", "protected": scenario != "unprotected branch"})
				default:
					t.Errorf("unexpected API %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			c := Config{BaseURL: ts.URL, ExpectedURL: ts.URL, ProjectID: "7", ExpectedProjectID: "7", PipelineID: "9", JobID: "11", CommitSHA: sha, Branch: "main", DefaultBranch: "main", Environment: "production", TargetEnvironmentID: "prod", TargetRegion: "us", JobToken: "job-token", ReadToken: "read-token", ProtectedRef: true, PipelineSource: "push"}
			v := Verifier{Config: c, HTTP: ts.Client()}
			target, region := "prod", "us"
			switch scenario {
			case "wrong target":
				target = "other"
			case "wrong region":
				region = "eu"
			}
			err := v.VerifyTarget(context.Background(), target, region)
			if scenario == "valid" && err != nil {
				t.Fatal(err)
			}
			if scenario != "valid" && err == nil {
				t.Fatal("unverified context accepted")
			}
		})
	}
}
