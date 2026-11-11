package gitlab

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab/gitlabtest"
)

type hostedTransport func(*http.Request) (*http.Response, error)

func (f hostedTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func privateSnapshotDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSnapshotPreservesExistingDestination(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	c := branchSubmitter(t, f).Client
	dir := privateSnapshotDir(t)
	p := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(p, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Snapshot(context.Background(), "main", dir, nil); err == nil {
		t.Error("snapshot accepted a nonempty caller directory")
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != "keep me" {
		t.Fatalf("snapshot destroyed caller content: %q, %v", b, err)
	}
}

func TestSnapshotSkipsRepositoryCIConfiguration(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{
		"main.tf":                      "x = 1\n",
		"customer_secrets.yml":         "secrets: []\n",
		".gitlab-ci.yml":               "include: enterprise.yml\n",
		".github/workflows/build.yaml": "jobs: {}\n",
		"gitlab-ci.yml":                "stages: [build]\n",
	})
	c := branchSubmitter(t, f).Client
	dir := privateSnapshotDir(t)
	if _, err := c.Snapshot(context.Background(), "main", dir, nil); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "main.tf")); err != nil || string(b) != "x = 1\n" {
		t.Fatalf("Terraform snapshot missing: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "customer_secrets.yml")); err != nil {
		t.Fatal("secret declarations must be included")
	}
	for _, name := range []string{".gitlab-ci.yml", ".github", "gitlab-ci.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("CI path was copied: %s", name)
		}
	}
}

func TestSnapshotRejectsSharedOrSymlinkDestination(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	c := branchSubmitter(t, f).Client
	shared := t.TempDir()
	if err := os.Chmod(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{shared, link} {
		if _, err := c.Snapshot(context.Background(), "main", dir, nil); err == nil {
			t.Errorf("snapshot accepted unsafe destination %s", dir)
		}
	}
}

func TestSnapshotUsesCapturedCommitDuringBranchMove(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n", "definitions/thing.tf": "y = 2\n", "dev.tfvars": "z = 3\n"})
	c := branchSubmitter(t, f).Client
	head := f.Head("main")
	transport := c.HTTP.Transport
	moved := false
	c.HTTP.Transport = hostedTransport(func(r *http.Request) (*http.Response, error) {
		if !moved && strings.HasSuffix(r.URL.Path, "/repository/tree") {
			moved = true
			f.Edit("main", "main.tf", "x = 99\n")
		}
		return transport.RoundTrip(r)
	})
	dir := privateSnapshotDir(t)
	got, err := c.Snapshot(context.Background(), "main", dir, []string{"definitions"})
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Errorf("snapshot base changed: got %s, want %s", got, head)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "main.tf")); err != nil || string(b) != "x = 1\n" {
		t.Errorf("snapshot contains moving branch content: %q, %v", b, err)
	}
	for _, line := range f.Requests {
		if strings.Contains(line, "/repository/tree?") || strings.Contains(line, "/repository/files/") {
			if !strings.Contains(line, "ref="+head) {
				t.Errorf("mutable snapshot read: %s", line)
			}
		}
	}
}

func hostedCommit(head string) BranchCommit {
	bc := requestCommit(head)
	bc.DeleteRequestFile = false
	return bc
}

func assertNoHostedWrites(t *testing.T, f *gitlabtest.Fake) {
	t.Helper()
	for _, line := range f.Requests {
		if !strings.HasPrefix(line, "GET ") {
			t.Errorf("unexpected remote mutation: %s", line)
		}
	}
}

func TestHostedValidationBeforeWrites(t *testing.T) {
	for name, mutate := range map[string]func(*BranchCommit, *gitlabtest.Fake){
		"wrong branch": func(b *BranchCommit, _ *gitlabtest.Fake) { b.Branch = "feature/foreign" },
		"invalid head": func(b *BranchCommit, _ *gitlabtest.Fake) { b.ExpectedHead = "invalid" },
		"forbidden file": func(b *BranchCommit, _ *gitlabtest.Fake) {
			b.Files = []File{{Path: ".gitlab-ci.yml", Content: []byte("x")}}
		},
		"public project":  func(_ *BranchCommit, f *gitlabtest.Fake) { f.Visibility = "public" },
		"changed default": func(_ *BranchCommit, f *gitlabtest.Fake) { f.Edit("main", "notes.txt", "new") },
	} {
		t.Run(name, func(t *testing.T) {
			f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
			s := branchSubmitter(t, f)
			bc := hostedCommit(f.Head("main"))
			mutate(&bc, f)
			if _, err := (HostedCommitter{S: s, DefaultBranch: "main"}).CommitToBranch(context.Background(), bc); err == nil {
				t.Error("invalid hosted publication accepted")
			}
			assertNoHostedWrites(t, f)
		})
	}
}

func TestHostedPreservesAcceptedCommitAfterLostResponse(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	s := branchSubmitter(t, f)
	bc := hostedCommit(f.Head("main"))
	transport := s.Client.HTTP.Transport
	s.Client.HTTP.Transport = hostedTransport(func(r *http.Request) (*http.Response, error) {
		res, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repository/commits") {
			res.Body.Close()
			return nil, errors.New("response lost after commit")
		}
		return res, err
	})
	_, err := (HostedCommitter{S: s, DefaultBranch: "main"}).CommitToBranch(context.Background(), bc)
	var partial *PartialError
	if !errors.As(err, &partial) || !partial.Resumable {
		t.Fatalf("want resumable partial outcome, got %v", err)
	}
	if !f.HasBranch(bc.Branch) || f.Head(bc.Branch) == bc.ExpectedHead {
		t.Fatal("hosted cleanup deleted an accepted commit")
	}
	for _, line := range f.Requests {
		if strings.HasPrefix(line, "DELETE ") {
			t.Errorf("destructive cleanup: %s", line)
		}
	}
}

func TestHostedPreservesConcurrentBranch(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	s := branchSubmitter(t, f)
	bc := hostedCommit(f.Head("main"))
	transport := s.Client.HTTP.Transport
	raced := false
	s.Client.HTTP.Transport = hostedTransport(func(r *http.Request) (*http.Response, error) {
		if !raced && r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/repository/branches") || strings.HasSuffix(r.URL.Path, "/repository/commits")) {
			raced = true
			f.CreateBranch(bc.Branch, "main", map[string]string{"notes.txt": "concurrent"})
		}
		return transport.RoundTrip(r)
	})
	_, err := (HostedCommitter{S: s, DefaultBranch: "main"}).CommitToBranch(context.Background(), bc)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if content, _, ok := f.File(bc.Branch, "notes.txt"); !ok || content != "concurrent" {
		t.Fatal("hosted cleanup deleted another writer's commit")
	}
}

func TestHostedRefusesUnrelatedExistingBranch(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	s := branchSubmitter(t, f)
	bc := hostedCommit(f.Head("main"))
	f.CreateBranch(bc.Branch, "main", nil)
	_, err := (HostedCommitter{S: s, DefaultBranch: "main"}).CommitToBranch(context.Background(), bc)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want unrelated branch conflict, got %v", err)
	}
	assertNoHostedWrites(t, f)
}

func TestHostedPublicationAndRetry(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	s := branchSubmitter(t, f)
	bc := hostedCommit(f.Head("main"))
	h := HostedCommitter{S: s, DefaultBranch: "main"}
	res, err := h.CommitToBranch(context.Background(), bc)
	if err != nil || res.CommitID == "" || res.MR == nil {
		t.Fatalf("publish: %+v, %v", res, err)
	}
	f.Edit("main", "notes.txt", "new default")
	_, err = h.CommitToBranch(context.Background(), bc)
	var done *AlreadyCommittedError
	if !errors.As(err, &done) || done.Result.CommitID != res.CommitID || len(f.MRs) != 1 {
		t.Fatalf("retry failed: %v", err)
	}
}

func TestHostedRetryNeverWritesContentAfterBranchRewind(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	s := branchSubmitter(t, f)
	bc := hostedCommit(f.Head("main"))
	h := HostedCommitter{S: s, DefaultBranch: "main"}
	res, err := h.CommitToBranch(context.Background(), bc)
	if err != nil {
		t.Fatal(err)
	}
	count := len(f.CommitOrder)
	transport := s.Client.HTTP.Transport
	rewound := false
	s.Client.HTTP.Transport = hostedTransport(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && !rewound && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/repository/branches/"+bc.Branch) {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			response.Body = io.NopCloser(bytes.NewReader(data))
			rewound = true
			// Another writer force-resets the branch after the retry has
			// read its already-submitted trailer.
			f.CreateBranch(bc.Branch, "main", nil)
		}
		return response, err
	})
	_, err = h.CommitToBranch(context.Background(), bc)
	var done *AlreadyCommittedError
	if !errors.As(err, &done) || done.Result.CommitID != res.CommitID {
		t.Errorf("retry: %v", err)
	}
	if len(f.CommitOrder) != count {
		t.Fatal("retry rewrote branch content after another writer rewound it")
	}
}

func TestHostedRecoversMergeRequestFailure(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	s := branchSubmitter(t, f)
	bc := hostedCommit(f.Head("main"))
	h := HostedCommitter{S: s, DefaultBranch: "main"}
	f.Fail["POST /merge_requests"] = http.StatusServiceUnavailable
	_, err := h.CommitToBranch(context.Background(), bc)
	var partial *PartialError
	if !errors.As(err, &partial) || partial.CommitID == "" || !f.HasBranch(bc.Branch) {
		t.Fatalf("want retained partial commit: %v", err)
	}
	count := len(f.CommitOrder)
	delete(f.Fail, "POST /merge_requests")
	_, err = h.CommitToBranch(context.Background(), bc)
	var done *AlreadyCommittedError
	if !errors.As(err, &done) || done.Result.MR == nil || done.Result.CommitID != partial.CommitID {
		t.Fatalf("retry: %v", err)
	}
	if len(f.CommitOrder) != count || len(f.MRs) != 1 {
		t.Fatal("MR recovery repeated the commit or MR")
	}
}
