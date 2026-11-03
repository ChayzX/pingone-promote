// Package gitlabtest is an in-memory fake of the GitLab REST API subset that
// pingone-promote uses: project and branch reads, repository files and tree,
// the Commits API with actions and last_commit_id checks, commit reverts and
// merge requests. It is used only from _test.go files.
package gitlabtest

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ProjectID is the fake project's numeric ID.
const ProjectID = "42"

// Token is the PRIVATE-TOKEN the fake requires.
const Token = "gitlabtest-token"

type fileState struct {
	Content    string
	LastCommit string
}

// Commit is a recorded commit.
type Commit struct {
	ID      string
	Branch  string
	Message string
	Actions []map[string]string
	// Before holds each touched path's content before the commit
	// (missing = absent), for reverts.
	Before map[string]*string
	After  map[string]*string
}

// MR is a recorded merge request.
type MR struct {
	ID, IID      int64
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
	Labels       string
	State        string
}

// Fake is the server state.
type Fake struct {
	*httptest.Server
	mu            sync.Mutex
	Visibility    string
	DefaultBranch string
	Protected     map[string]bool
	branches      map[string]map[string]fileState // branch -> path -> file
	heads         map[string]string               // branch -> commit ID
	Commits       map[string]*Commit
	CommitOrder   []string
	MRs           []*MR
	// Fail makes requests whose path ends with the key return the status.
	Fail map[string]int
	// Redirect makes requests whose path ends with the key redirect.
	Redirect map[string]string
	Requests []string
	// RaceBeforeCommit, when set, is committed to the default branch just
	// before the next Commits API call is processed: a concurrent edit
	// between pingone-promote's read and its commit.
	RaceBeforeCommit map[string]string
	seq              int
}

// New starts a fake with a protected, private "main" holding files.
func New(t testing.TB, files map[string]string) *Fake {
	t.Helper()
	f := &Fake{Visibility: "private", DefaultBranch: "main", Protected: map[string]bool{"main": true},
		branches: map[string]map[string]fileState{"main": {}}, heads: map[string]string{}, Commits: map[string]*Commit{},
		Fail: map[string]int{}, Redirect: map[string]string{}}
	if len(files) > 0 {
		actions := []map[string]string{}
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			actions = append(actions, map[string]string{"action": "create", "file_path": p, "content": files[p]})
		}
		if _, err := f.commit("main", "Initial import", actions); err != "" {
			t.Fatal(err)
		}
	}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

// BaseURL is the GitLab base URL for gitlab.Config.
func (f *Fake) BaseURL() string { return f.URL }

// File returns a file's content and last commit on a branch.
func (f *Fake) File(branch, path string) (content, lastCommit string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.branches[branch][path]
	return st.Content, st.LastCommit, ok
}

// Head returns a branch's head commit.
func (f *Fake) Head(branch string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.heads[branch]
}

// HasBranch reports whether a branch exists.
func (f *Fake) HasBranch(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.branches[name]
	return ok
}

// Merge fast-forwards the default branch to branch (a merged MR).
func (f *Fake) Merge(branch string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	files := map[string]fileState{}
	for k, v := range f.branches[branch] {
		files[k] = v
	}
	f.branches[f.DefaultBranch] = files
	f.heads[f.DefaultBranch] = f.heads[branch]
	for _, mr := range f.MRs {
		if mr.SourceBranch == branch {
			mr.State = "merged"
		}
	}
}

// Edit commits a change to a branch outside pingone-promote.
func (f *Fake) Edit(branch, path, content string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	action := "update"
	if _, ok := f.branches[branch][path]; !ok {
		action = "create"
	}
	id, _ := f.commit(branch, "Manual edit", []map[string]string{{"action": action, "file_path": path, "content": content}})
	return id
}

func (f *Fake) commit(branch, message string, actions []map[string]string) (string, string) {
	files := f.branches[branch]
	c := &Commit{Branch: branch, Message: message, Actions: actions, Before: map[string]*string{}, After: map[string]*string{}}
	next := map[string]fileState{}
	for k, v := range files {
		next[k] = v
	}
	f.seq++
	sum := sha1.Sum([]byte(fmt.Sprintf("%d %s %s", f.seq, branch, message)))
	c.ID = hex.EncodeToString(sum[:])
	for _, a := range actions {
		p := a["file_path"]
		cur, exists := next[p]
		if exists {
			v := cur.Content
			c.Before[p] = &v
		}
		switch a["action"] {
		case "create":
			if exists {
				return "", "A file with this name already exists"
			}
		case "update":
			if !exists {
				return "", "A file with this name doesn't exist"
			}
			if id, ok := a["last_commit_id"]; ok && id != cur.LastCommit {
				return "", "You are attempting to update a file that has changed since you started editing it."
			}
		case "delete":
			if !exists {
				return "", "A file with this name doesn't exist"
			}
			delete(next, p)
			c.After[p] = nil
			continue
		default:
			return "", "unknown action"
		}
		content := a["content"]
		next[p] = fileState{Content: content, LastCommit: c.ID}
		c.After[p] = &content
	}
	f.branches[branch] = next
	f.heads[branch] = c.ID
	f.Commits[c.ID] = c
	f.CommitOrder = append(f.CommitOrder, c.ID)
	return c.ID, ""
}

func (f *Fake) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *Fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests = append(f.Requests, r.Method+" "+r.URL.RequestURI())
	if r.Header.Get("PRIVATE-TOKEN") != Token {
		f.reply(w, 401, map[string]string{"message": "401 Unauthorized"})
		return
	}
	for suffix, to := range f.Redirect {
		if strings.HasSuffix(r.URL.Path, suffix) {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
	}
	for key, status := range f.Fail {
		// A key is "<path suffix>" or "<METHOD> <path suffix>".
		method, suffix, scoped := strings.Cut(key, " ")
		if !scoped {
			method, suffix = r.Method, key
		}
		if method == r.Method && strings.HasSuffix(r.URL.Path, suffix) {
			f.reply(w, status, map[string]string{"message": "injected failure"})
			return
		}
	}
	prefix := "/api/v4/projects/" + ProjectID
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), prefix)
	if !ok {
		f.reply(w, 404, map[string]string{"message": "404 Project Not Found"})
		return
	}
	var body map[string]any
	if r.Body != nil && r.Method != http.MethodGet {
		json.NewDecoder(r.Body).Decode(&body)
	}
	str := func(k string) string { s, _ := body[k].(string); return s }
	switch {
	case rest == "" && r.Method == http.MethodGet:
		f.reply(w, 200, map[string]any{"id": 42, "visibility": f.Visibility, "archived": false, "default_branch": f.DefaultBranch,
			"web_url": "https://" + r.Host + "/group/infra"})
	case strings.HasPrefix(rest, "/repository/branches/") && r.Method == http.MethodGet:
		name, _ := url.PathUnescape(strings.TrimPrefix(rest, "/repository/branches/"))
		if _, ok := f.branches[name]; !ok {
			f.reply(w, 404, map[string]string{"message": "404 Branch Not Found"})
			return
		}
		head := f.Commits[f.heads[name]]
		msg := ""
		if head != nil {
			msg = head.Message
		}
		f.reply(w, 200, map[string]any{"name": name, "protected": f.Protected[name], "commit": map[string]string{"id": f.heads[name], "message": msg}})
	case rest == "/repository/branches" && r.Method == http.MethodPost:
		name, ref := str("branch"), str("ref")
		if _, exists := f.branches[name]; exists {
			f.reply(w, 400, map[string]string{"message": "Branch already exists"})
			return
		}
		src, ok := f.branches[ref]
		if !ok {
			f.reply(w, 404, map[string]string{"message": "Invalid reference name"})
			return
		}
		cp := map[string]fileState{}
		for k, v := range src {
			cp[k] = v
		}
		f.branches[name], f.heads[name] = cp, f.heads[ref]
		f.reply(w, 201, map[string]any{"name": name})
	case strings.HasPrefix(rest, "/repository/files/") && r.Method == http.MethodGet:
		p, _ := url.PathUnescape(strings.TrimPrefix(rest, "/repository/files/"))
		st, ok := f.branches[r.URL.Query().Get("ref")][p]
		if !ok {
			f.reply(w, 404, map[string]string{"message": "404 File Not Found"})
			return
		}
		f.reply(w, 200, map[string]any{"file_path": p, "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(st.Content)),
			"blob_id": "blob-" + st.LastCommit[:8], "last_commit_id": st.LastCommit})
	case rest == "/repository/tree" && r.Method == http.MethodGet:
		dir := r.URL.Query().Get("path")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var entries []map[string]string
		seen := map[string]bool{}
		for p := range f.branches[r.URL.Query().Get("ref")] {
			rel, ok := strings.CutPrefix(p, dir+"/")
			if dir == "" {
				rel, ok = p, true
			}
			if !ok {
				continue
			}
			name, _, isDir := strings.Cut(rel, "/")
			if seen[name] {
				continue
			}
			seen[name] = true
			typ := "blob"
			if isDir {
				typ = "tree"
			}
			full := name
			if dir != "" {
				full = dir + "/" + name
			}
			entries = append(entries, map[string]string{"name": name, "path": full, "type": typ})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i]["path"] < entries[j]["path"] })
		if page > 1 {
			entries = nil
		}
		f.reply(w, 200, entries)
	case rest == "/repository/commits" && r.Method == http.MethodPost:
		for p, content := range f.RaceBeforeCommit {
			action := "update"
			if _, ok := f.branches[f.DefaultBranch][p]; !ok {
				action = "create"
			}
			f.commit(f.DefaultBranch, "Concurrent edit", []map[string]string{{"action": action, "file_path": p, "content": content}})
		}
		f.RaceBeforeCommit = nil
		branch, start := str("branch"), str("start_branch")
		if start != "" {
			if _, exists := f.branches[branch]; exists {
				f.reply(w, 400, map[string]string{"message": "A branch called '" + branch + "' already exists"})
				return
			}
			src, ok := f.branches[start]
			if !ok {
				f.reply(w, 400, map[string]string{"message": "invalid start branch"})
				return
			}
			cp := map[string]fileState{}
			for k, v := range src {
				cp[k] = v
			}
			f.branches[branch], f.heads[branch] = cp, f.heads[start]
		} else if _, ok := f.branches[branch]; !ok {
			f.reply(w, 400, map[string]string{"message": "You can only create or edit files when you are on a branch"})
			return
		}
		var actions []map[string]string
		raw, _ := json.Marshal(body["actions"])
		json.Unmarshal(raw, &actions)
		id, errMsg := f.commit(branch, str("commit_message"), actions)
		if errMsg != "" {
			if start != "" {
				delete(f.branches, branch)
				delete(f.heads, branch)
			}
			f.reply(w, 400, map[string]string{"message": errMsg})
			return
		}
		f.reply(w, 201, map[string]any{"id": id, "short_id": id[:8], "web_url": "https://" + r.Host + "/group/infra/-/commit/" + id})
	case strings.HasPrefix(rest, "/repository/commits/") && strings.HasSuffix(rest, "/revert") && r.Method == http.MethodPost:
		sha := strings.TrimSuffix(strings.TrimPrefix(rest, "/repository/commits/"), "/revert")
		orig, ok := f.Commits[sha]
		branch := str("branch")
		files, bok := f.branches[branch]
		if !ok || !bok {
			f.reply(w, 404, map[string]string{"message": "404 Commit Not Found"})
			return
		}
		var actions []map[string]string
		for p, after := range orig.After {
			cur, exists := files[p]
			if after == nil || !exists || cur.Content != *after {
				f.reply(w, 400, map[string]string{"message": "Sorry, we cannot revert this commit automatically."})
				return
			}
			if before := orig.Before[p]; before != nil {
				actions = append(actions, map[string]string{"action": "update", "file_path": p, "content": *before})
			} else {
				actions = append(actions, map[string]string{"action": "delete", "file_path": p})
			}
		}
		sort.Slice(actions, func(i, j int) bool { return actions[i]["file_path"] < actions[j]["file_path"] })
		id, errMsg := f.commit(branch, "Revert \""+strings.SplitN(orig.Message, "\n", 2)[0]+"\"\n\nThis reverts commit "+sha+".", actions)
		if errMsg != "" {
			f.reply(w, 400, map[string]string{"message": errMsg})
			return
		}
		f.reply(w, 201, map[string]any{"id": id})
	case rest == "/merge_requests" && r.Method == http.MethodGet:
		var out []map[string]any
		for _, mr := range f.MRs {
			if mr.SourceBranch == r.URL.Query().Get("source_branch") && mr.State == "opened" {
				out = append(out, f.mrJSON(r, mr))
			}
		}
		f.reply(w, 200, out)
	case rest == "/merge_requests" && r.Method == http.MethodPost:
		mr := &MR{ID: int64(100 + len(f.MRs)), IID: int64(1 + len(f.MRs)), SourceBranch: str("source_branch"), TargetBranch: str("target_branch"),
			Title: str("title"), Description: str("description"), Labels: str("labels"), State: "opened"}
		if _, ok := f.branches[mr.SourceBranch]; !ok {
			f.reply(w, 400, map[string]string{"message": "source branch does not exist"})
			return
		}
		f.MRs = append(f.MRs, mr)
		f.reply(w, 201, f.mrJSON(r, mr))
	default:
		f.reply(w, 404, map[string]string{"message": "404 Not Found"})
	}
}

func (f *Fake) mrJSON(r *http.Request, mr *MR) map[string]any {
	return map[string]any{"id": mr.ID, "iid": mr.IID, "source_branch": mr.SourceBranch,
		"web_url": "https://" + r.Host + "/group/infra/-/merge_requests/" + strconv.FormatInt(mr.IID, 10)}
}

// CreateBranch creates branch from another branch's head, as DaVinci does
// with the Commits API (start_branch). It returns the new head.
func (f *Fake) CreateBranch(name, from string, files map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[string]fileState{}
	for k, v := range f.branches[from] {
		cp[k] = v
	}
	f.branches[name], f.heads[name] = cp, f.heads[from]
	if len(files) == 0 {
		return f.heads[name]
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var actions []map[string]string
	for _, p := range paths {
		action := "create"
		if _, ok := cp[p]; ok {
			action = "update"
		}
		actions = append(actions, map[string]string{"action": action, "file_path": p, "content": files[p]})
	}
	id, _ := f.commit(name, "Add pingone-promote request", actions)
	return id
}

// Files returns every file on a branch.
func (f *Fake) Files(branch string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for p, s := range f.branches[branch] {
		out[p] = s.Content
	}
	return out
}
