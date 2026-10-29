package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(serverURL string) Config {
	return Config{BaseURL: serverURL, ProjectID: "42", TokenEnv: "GITLAB_TOKEN", Environment: "production"}
}

func TestConfigValidate(t *testing.T) {
	valid := testConfig("https://git.example/gitlab")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	cases := []Config{
		{BaseURL: "http://git.example", ProjectID: "42"},
		{BaseURL: "https://user:pass@git.example", ProjectID: "42"},
		{BaseURL: "https://git.example/?x=1", ProjectID: "42"},
		{BaseURL: "https://git.example/#frag", ProjectID: "42"},
		{BaseURL: "https://git.example/a/../b", ProjectID: "42"},
		{BaseURL: "https://git.example/gitlab%2f..%2foutside", ProjectID: "42"},
		{BaseURL: "https://git.example", ProjectID: "group/project"},
		{BaseURL: "https://git.example", ProjectID: "42", TokenEnv: "BAD-NAME"},
		{BaseURL: "https://git.example", ProjectID: "42", Environment: "prod/evil"},
	}
	for _, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("accepted invalid config %#v", c)
		}
	}
}

func TestDoPreservesSubpathAndSanitizesFailures(t *testing.T) {
	const token = "secret-token-for-test"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gitlab/api/v4/projects/42" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("PRIVATE-TOKEN") != token {
			t.Errorf("missing private token header")
		}
		http.Error(w, token+" private response body", http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := NewClient(testConfig(srv.URL+"/gitlab"), token)
	if err != nil {
		t.Fatal(err)
	}
	// Use the test TLS transport while retaining the client's redirect policy.
	c.HTTP = srv.Client()
	err = c.Do(context.Background(), http.MethodGet, "projects/42", nil, nil)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "private response") {
		t.Fatalf("unsafe status error: %v", err)
	}
}

func TestDoRejectsEscapingPathsAndRedirects(t *testing.T) {
	outside := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("outside origin received request") }))
	defer outside.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, outside.URL+"/steal", http.StatusFound)
	}))
	defer origin.Close()
	c, err := NewClient(testConfig(origin.URL), "secret")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = origin.Client()
	for _, path := range []string{"https://evil.invalid/api", "../evil", "%2e%2e/evil", "/api/v4/projects/42"} {
		if err := c.Do(context.Background(), http.MethodGet, path, nil, nil); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	err = c.Do(context.Background(), http.MethodGet, "projects/42", nil, nil)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect/error handling: %v", err)
	}
}

func TestVerifyProjectRequiresPrivateProtectedDefaultBranch(t *testing.T) {
	for _, tc := range []struct {
		visibility string
		protected  bool
		wantErr    bool
	}{{"private", true, false}, {"public", true, true}, {"private", false, true}} {
		var serverURL string
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/repository/branches/main") {
				_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": tc.protected})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "visibility": tc.visibility, "archived": false, "default_branch": "main", "web_url": serverURL + "/gitlab/group/project"})
		}))
		serverURL = srv.URL
		c, err := NewClient(testConfig(srv.URL+"/gitlab"), "t")
		if err != nil {
			t.Fatal(err)
		}
		c.HTTP = srv.Client()
		_, err = c.VerifyProject(context.Background())
		srv.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("visibility=%s protected=%v err=%v", tc.visibility, tc.protected, err)
		}
	}
}

func TestSubmitCreatesScopedBranchCommitAndMergeRequest(t *testing.T) {
	var gotBranch, gotCommit, gotMR bool
	branchExists := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/projects/42"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "visibility": "private", "archived": false, "default_branch": "main", "web_url": srvURLPlaceholder})
		case strings.HasSuffix(r.URL.Path, "/repository/branches/main"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": true})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/repository/branches"):
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["branch"] != "promotion/req-1" || b["ref"] != "main" {
				t.Errorf("branch payload: %#v", b)
			}
			gotBranch = true
			branchExists = true
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"name":"promotion/req-1"}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/repository/commits"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotCommit = true
			if !branchExists {
				t.Error("commit attempted before promotion branch was created")
			}
			if b["branch"] != "promotion/req-1" {
				t.Errorf("commit refs: %#v", b)
			}
			if _, ok := b["start_branch"]; ok || b["force"] == true {
				// GitLab's create-commit API treats start_branch/force as branch
				// creation or replacement controls; this branch already exists.
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"A branch called 'promotion/req-1' already exists"}`))
				return
			}
			actions, _ := b["actions"].([]any)
			if len(actions) != 1 {
				t.Errorf("actions: %#v", actions)
			} else {
				a := actions[0].(map[string]any)
				if a["file_path"] != "requests/req-1/request.json" || a["content"] != "{}" {
					t.Errorf("action: %#v", a)
				}
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"commit"}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			gotMR = true
			if b["target_branch"] != "main" || b["source_branch"] != "promotion/req-1" || b["remove_source_branch"] != true || b["squash"] != false {
				t.Errorf("MR payload: %#v", b)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":8,"iid":9,"web_url":"` + srvURLPlaceholder + `/group/project/-/merge_requests/9"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURLPlaceholder = srv.URL + "/gitlab"
	c, err := NewClient(testConfig(srv.URL+"/gitlab"), "token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = srv.Client()
	mr, err := c.Submit(context.Background(), "req-1", map[string]string{"request.json": "{}"}, "title", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if mr.IID != 9 || !gotBranch || !gotCommit || !gotMR {
		t.Fatalf("result=%#v calls=%v/%v/%v", mr, gotBranch, gotCommit, gotMR)
	}
}

func TestSubmitBranchCollisionStopsBeforeCommitOrMergeRequest(t *testing.T) {
	var serverURL string
	var gotCommit, gotMR bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/projects/42"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "visibility": "private", "archived": false, "default_branch": "main", "web_url": serverURL + "/gitlab/group/project"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repository/branches/main"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/branches"):
			// The request-specific branch already exists. GitLab rejects branch
			// creation; the client must leave it untouched and stop here.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Branch already exists"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/commits"):
			gotCommit = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			gotMR = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL
	c, err := NewClient(testConfig(srv.URL+"/gitlab"), "token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = srv.Client()
	if _, err = c.Submit(context.Background(), "req-1", map[string]string{"request.json": "{}"}, "title", "desc"); err == nil {
		t.Fatal("preexisting promotion branch was accepted")
	}
	if gotCommit || gotMR {
		t.Fatalf("branch collision did not abort before later writes: commit=%v merge_request=%v", gotCommit, gotMR)
	}
}

var srvURLPlaceholder string

func TestSubmitRejectsUnsafeFileAndMRURL(t *testing.T) {
	for _, name := range []string{"../x", ".gitlab-ci.yml", "requests/elsewhere/x"} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		c, _ := NewClient(testConfig(srv.URL), "token")
		c.HTTP = srv.Client()
		if _, err := c.Submit(context.Background(), "req", map[string]string{name: "x"}, "t", "d"); err == nil {
			t.Errorf("accepted file %q", name)
		}
		srv.Close()
	}
}

func TestSubmitRejectsMalformedMergeRequestResponse(t *testing.T) {
	var serverURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/projects/42"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "visibility": "private", "archived": false, "default_branch": "main", "web_url": serverURL + "/gitlab/group/project"})
		case strings.HasSuffix(r.URL.Path, "/repository/branches/main"):
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/branches"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/commits"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"web_url":"` + serverURL + `/gitlab/group/project"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	serverURL = srv.URL
	c, err := NewClient(testConfig(srv.URL+"/gitlab"), "token")
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = srv.Client()
	if _, err = c.Submit(context.Background(), "req", map[string]string{"request.json": "{}"}, "title", "description"); err == nil {
		t.Fatal("malformed merge request response reported success")
	}
}

func TestConfigPersistenceIsPrivateAndMetadataOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	c := testConfig("https://git.example/gitlab")
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir)
	if err != nil || got != c {
		t.Fatalf("load = %#v, %v", got, err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0700 {
		t.Errorf("dir mode %o", st.Mode().Perm())
	}
	st, err = os.Stat(filepath.Join(dir, "gitlab.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Errorf("file mode %o", st.Mode().Perm())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "gitlab.json"))
	if strings.Contains(string(b), "secret-token") {
		t.Errorf("unexpected token value in config: %s", b)
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing config: %v", err)
	}
}

func TestSafeMergeRequestURLConfinement(t *testing.T) {
	base, _ := url.Parse("https://git.example/gitlab")
	for _, raw := range []string{"https://evil.example/gitlab/x", "https://git.example/other/x", "https://git.example/gitlab/%2e%2e/x", "https://git.example/gitlab%2f..%2foutside/x"} {
		u, _ := url.Parse(raw)
		if sameGitLabLocation(base, u) {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestConfigUsesExpectedJSONNames(t *testing.T) {
	b, err := json.Marshal(Config{BaseURL: "https://git.example/gitlab", ProjectID: "42"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, field := range []string{`"baseUrl"`, `"projectId"`} {
		if !strings.Contains(text, field) {
			t.Errorf("JSON %s missing from %s", field, text)
		}
	}
	for _, field := range []string{`"baseURL"`, `"projectID"`} {
		if strings.Contains(text, field) {
			t.Errorf("unexpected legacy JSON field %s", field)
		}
	}
}

func TestLoadConfigRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, content := range []string{
		`{"baseUrl":"https://git.example","projectId":"42","token":"secret"}`,
		`{"baseUrl":"https://git.example","projectId":"42"} {}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "gitlab.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(dir); err == nil {
			t.Errorf("accepted config %s", content)
		}
	}
}

func TestSaveConfigPersistsEffectiveDefaults(t *testing.T) {
	dir := t.TempDir()
	c := Config{BaseURL: "https://git.example", ProjectID: "42"}
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenEnv != "GITLAB_TOKEN" || got.Environment != "production" {
		t.Fatalf("persisted config defaults: %#v", got)
	}
}

func TestVerifyProjectRequiresConfiguredIdentityAndConfinedWebURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      int64
		webURL  string
		wantErr bool
	}{
		{"matching", 42, "", false},
		{"wrong identity", 43, "", true},
		{"hostile URL", 42, "https://evil.example/gitlab/group/project", true},
		{"outside subpath", 42, "outside", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/repository/branches/main") {
					_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "protected": true})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": tc.id, "visibility": "private", "archived": false, "default_branch": "main", "web_url": tc.webURL})
			}))
			defer srv.Close()
			if tc.webURL == "" {
				tc.webURL = srv.URL + "/gitlab/group/project"
			}
			if tc.webURL == "outside" {
				tc.webURL = srv.URL + "/other/group/project"
			}
			c, err := NewClient(testConfig(srv.URL+"/gitlab"), "token")
			if err != nil {
				t.Fatal(err)
			}
			c.HTTP = srv.Client()
			p, err := c.VerifyProject(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("project=%#v err=%v", p, err)
			}
		})
	}
}
