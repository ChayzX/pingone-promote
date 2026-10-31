package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab"
)

func TestGitLabPreviewSubmitsOnlyReviewedRequestToPrivateProject(t *testing.T) {
	h := setup(t)
	s := h.server.Config.Handler.(*Server)
	s.localTargets = map[string]bool{}
	var committed map[string]any
	var createdBranch map[string]string
	gl := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("PRIVATE-TOKEN") != "fixture-gitlab-token" {
			t.Error("GitLab credential missing")
		}
		switch r.URL.Path {
		case "/api/v4/projects/7":
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "visibility": "private", "default_branch": "main", "web_url": "https://" + r.Host + "/group/private-project"})
		case "/api/v4/projects/7/repository/branches/main":
			w.Write([]byte(`{"name":"main","protected":true}`))
		case "/api/v4/projects/7/repository/branches":
			_ = json.NewDecoder(r.Body).Decode(&createdBranch)
			w.Write([]byte(`{}`))
		case "/api/v4/projects/7/repository/commits":
			json.NewDecoder(r.Body).Decode(&committed)
			w.Write([]byte(`{}`))
		case "/api/v4/projects/7/merge_requests":
			json.NewEncoder(w).Encode(map[string]any{"id": 8, "iid": 1, "web_url": "https://" + r.Host + "/group/private-project/-/merge_requests/1"})
		default:
			t.Errorf("unexpected GitLab call %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer gl.Close()
	s.gitlabFactory = func(c gitlab.Config, token string) (*gitlab.Client, error) {
		client, err := gitlab.NewClient(c, token)
		if client != nil {
			client.HTTP = gl.Client()
		}
		return client, err
	}
	h.call("POST", "/api/gitlab", map[string]string{"baseUrl": gl.URL, "projectId": "7", "environment": "production", "token": "fixture-gitlab-token"}, nil, 200)
	raw, err := os.ReadFile(filepath.Join(s.store.Directory(), "gitlab.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-gitlab-token") {
		t.Fatal("GitLab token persisted")
	}
	var a, b struct {
		ID string `json:"id"`
	}
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.source.ID}, &a, 200)
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.target.ID}, &b, 200)
	var preview struct {
		Request struct {
			ID string `json:"id"`
		} `json:"request"`
		Required bool `json:"gitlabRequired"`
	}
	h.call("POST", "/api/requests/preview", map[string]any{"sourceInventoryId": a.ID, "targetInventoryId": b.ID, "selected": []string{"f"}, "mappings": map[string]string{"f": "g", "c": "d"}, "description": "approved request"}, &preview, 200)
	if !preview.Required || preview.Request.ID == "" {
		t.Fatal("production request missing preview")
	}
	var result struct {
		ID  string `json:"requestId"`
		URL string `json:"url"`
	}
	h.call("POST", "/api/gitlab/submit", map[string]string{"requestId": preview.Request.ID, "branchName": "requests/custom-review", "commitMessage": "Review promotion request"}, &result, 200)
	if result.ID != preview.Request.ID || !strings.Contains(result.URL, "/merge_requests/1") {
		t.Fatalf("bad submission %+v", result)
	}
	actions := committed["actions"].([]any)
	if createdBranch["branch"] != "requests/custom-review" || committed["commit_message"] != "Review promotion request" {
		t.Fatalf("custom branch/message ignored: branch=%#v commit=%#v", createdBranch, committed["commit_message"])
	}
	action := actions[0].(map[string]any)
	if action["file_path"] != "requests/"+preview.Request.ID+"/request.json" {
		t.Fatal("unexpected commit path")
	}
	content := action["content"].(string)
	if !strings.Contains(content, `"id": "`+preview.Request.ID+`"`) || strings.Contains(content, "fixture-gitlab-token") || strings.Contains(content, `"clientId"`) {
		t.Fatalf("request changed or leaked credentials: %s", content)
	}
	h.call("POST", "/api/gitlab/submit", map[string]string{"requestId": preview.Request.ID}, nil, 409)
	if h.f.created != nil || len(h.f.executed) != 0 {
		t.Fatal("GitLab submission changed PingOne configuration")
	}
}
func TestUnknownGitLabPreviewCannotBeSubmitted(t *testing.T) {
	h := setup(t)
	h.call("POST", "/api/gitlab/submit", map[string]string{"requestId": "invented"}, nil, 400)
}

func TestGitLabPreflightFailureCanBeRetriedWithoutRebuildingPreview(t *testing.T) {
	h := setup(t)
	s := h.server.Config.Handler.(*Server)
	var rejectProjectReads bool
	var rejectBranchWrite bool
	var branchWrites int
	var serverURL string
	gl := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/projects/7":
			if rejectProjectReads {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "visibility": "private", "default_branch": "main", "web_url": serverURL + "/group/private-project"})
		case "/api/v4/projects/7/repository/branches/main":
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case "/api/v4/projects/7/repository/branches":
			branchWrites++
			if rejectBranchWrite {
				http.Error(w, "temporary failure", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case "/api/v4/projects/7/repository/commits":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case "/api/v4/projects/7/merge_requests":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":8,"iid":9,"web_url":"` + serverURL + `/group/private-project/-/merge_requests/9"}`))
		default:
			t.Errorf("unexpected GitLab call %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer gl.Close()
	serverURL = gl.URL
	s.gitlabFactory = func(c gitlab.Config, token string) (*gitlab.Client, error) {
		client, err := gitlab.NewClient(c, token)
		if client != nil {
			client.HTTP = gl.Client()
		}
		return client, err
	}
	h.call("POST", "/api/gitlab", map[string]string{"baseUrl": gl.URL, "projectId": "7", "environment": "production", "token": "fixture-gitlab-token"}, nil, 200)
	var a, b struct {
		ID string `json:"id"`
	}
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.source.ID}, &a, 200)
	h.call("POST", "/api/inventory", map[string]string{"profileId": h.target.ID}, &b, 200)
	var preview struct {
		Request struct {
			ID string `json:"id"`
		} `json:"request"`
	}
	h.call("POST", "/api/requests/preview", map[string]any{"sourceInventoryId": a.ID, "targetInventoryId": b.ID, "selected": []string{"f"}, "mappings": map[string]string{"f": "g", "c": "d"}}, &preview, 200)

	rejectProjectReads = true
	var failure struct {
		Error       string `json:"error"`
		SafeToRetry bool   `json:"safeToRetry"`
	}
	h.call("POST", "/api/gitlab/submit", map[string]string{"requestId": preview.Request.ID}, &failure, 502)
	if !failure.SafeToRetry || !strings.Contains(strings.ToLower(failure.Error), "no gitlab writes occurred") {
		t.Fatalf("preflight failure did not explain that no writes occurred: %q", failure.Error)
	}
	if branchWrites != 0 {
		t.Fatalf("preflight failure made %d branch writes", branchWrites)
	}

	rejectProjectReads = false
	rejectBranchWrite = true
	var writeFailure map[string]json.RawMessage
	h.call("POST", "/api/gitlab/submit", map[string]string{"requestId": preview.Request.ID}, &writeFailure, 502)
	if _, safeToRetryPresent := writeFailure["safeToRetry"]; safeToRetryPresent || branchWrites != 1 {
		t.Fatalf("write failure safeToRetry present=%v branch writes=%d", safeToRetryPresent, branchWrites)
	}
	h.call("POST", "/api/gitlab/submit", map[string]string{"requestId": preview.Request.ID}, nil, 409)
	if branchWrites != 1 {
		t.Fatalf("uncertain retry made another branch write: %d", branchWrites)
	}
}
