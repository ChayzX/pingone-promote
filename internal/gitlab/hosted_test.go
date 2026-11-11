package gitlab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab/gitlabtest"
)

func snapshotClient(t *testing.T, f *gitlabtest.Fake) *Client {
	t.Helper()
	c, err := NewClient(Config{BaseURL: f.BaseURL(), ProjectID: gitlabtest.ProjectID, TokenEnv: "T"}, gitlabtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = f.Client()
	return c
}

// privateTemp points the system temporary directory at a test directory, so
// a test can see what Snapshot leaves behind.
func privateTemp(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	return tmp
}

var snapshotRepo = map[string]string{
	"main.tf":                     "module \"davinci\" {}\n",
	"qa.tfvars":                   "a = 1\n",
	"customer_secrets.yml":        "secrets: []\n",
	"notes.md":                    "not read\n",
	".gitlab-ci.yml":              "not read\n",
	"definitions/a.tf":            "a = \"old\"\n",
	"definitions/b.tf":            "b = \"old\"\n",
	"definitions/nested/deep.tf":  "not read\n",
	"other/x.tf":                  "not read\n",
	".pingone-promote/request.tf": "not read\n",
}

func TestSnapshotReadsOneCommitDespiteConcurrentPush(t *testing.T) {
	tmp := privateTemp(t)
	f := gitlabtest.New(t, snapshotRepo)
	before := f.Head("main")
	// A push lands between the read of a.tf and the read of b.tf.
	f.RaceOnRequest = map[string]map[string]string{"GET /repository/files/definitions/b.tf": {
		"definitions/a.tf": "a = \"new\"\n", "definitions/b.tf": "b = \"new\"\n"}}
	dir, head, err := snapshotClient(t, f).Snapshot(context.Background(), "main", []string{"definitions"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if f.Head("main") == before {
		t.Fatal("the concurrent push did not happen")
	}
	if head != before {
		t.Fatalf("head %s, want the commit resolved first (%s)", head, before)
	}
	for p, want := range map[string]string{"definitions/a.tf": "a = \"old\"\n", "definitions/b.tf": "b = \"old\"\n", "main.tf": snapshotRepo["main.tf"],
		"qa.tfvars": snapshotRepo["qa.tfvars"], "customer_secrets.yml": snapshotRepo["customer_secrets.yml"]} {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q (%v), want %q: versions were mixed", p, got, err, want)
		}
	}
	for _, p := range []string{"notes.md", ".gitlab-ci.yml", "definitions/nested/deep.tf", "other/x.tf", ".pingone-promote/request.tf"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil {
			t.Fatalf("%s is outside the allow-list but was read", p)
		}
	}
	// Every tree and file read named the SHA, never the branch.
	for _, r := range f.Requests {
		if strings.Contains(r, "/repository/tree") || strings.Contains(r, "/repository/files/") {
			if !strings.Contains(r, "ref="+before) {
				t.Fatalf("read at a moving ref: %s", r)
			}
		}
	}
	if filepath.Dir(dir) != filepath.Clean(tmp) {
		t.Fatalf("snapshot directory %s is not a new directory under the temporary directory", dir)
	}
}

func TestSnapshotDirectoryIsPrivate(t *testing.T) {
	privateTemp(t)
	f := gitlabtest.New(t, snapshotRepo)
	dir, _, err := snapshotClient(t, f).Snapshot(context.Background(), "main", []string{"definitions"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot directory mode %v (%v), want 0700", fi.Mode().Perm(), err)
	}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if want := os.FileMode(0o600); !fi.IsDir() && fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
		if want := os.FileMode(0o700); fi.IsDir() && fi.Mode().Perm() != want {
			t.Fatalf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
		return nil
	})
}

func TestSnapshotLimitsAndCleanup(t *testing.T) {
	tmp := privateTemp(t)
	f := gitlabtest.New(t, snapshotRepo)
	c := snapshotClient(t, f)
	for name, lim := range map[string]snapshotLimits{"files": {Files: 2, Bytes: 1 << 20}, "bytes": {Files: 100, Bytes: 10}} {
		t.Run(name, func(t *testing.T) {
			dir, head, err := c.snapshot(context.Background(), "main", []string{"definitions"}, lim)
			if err == nil || dir != "" || head != "" {
				t.Fatalf("limit not enforced: %q %q %v", dir, head, err)
			}
			left, _ := os.ReadDir(tmp)
			if len(left) != 0 {
				t.Fatalf("a failed snapshot left %d entries behind", len(left))
			}
		})
	}
}

func TestSnapshotRefusesPathsOutsideAllowList(t *testing.T) {
	privateTemp(t)
	f := gitlabtest.New(t, snapshotRepo)
	c := snapshotClient(t, f)
	for _, d := range []string{"..", "../etc", "/etc", ".pingone-promote", "definitions/../..", "a\\b", ".gitlab-ci.yml"} {
		if dir, _, err := c.Snapshot(context.Background(), "main", []string{d}); err == nil {
			os.RemoveAll(dir)
			t.Fatalf("directory %q accepted", d)
		}
	}
	if _, _, err := c.Snapshot(context.Background(), "absent", nil); err == nil {
		t.Fatal("missing branch accepted")
	}
}

// hostedSetup is a repository whose default branch is the previewed SHA.
func hostedSetup(t *testing.T) (*gitlabtest.Fake, HostedCommitter, BranchCommit) {
	t.Helper()
	f := gitlabtest.New(t, map[string]string{"main.tf": "module \"davinci\" {\n  source = \"./definitions\"\n}\n"})
	h := HostedCommitter{S: branchSubmitter(t, f), DefaultBranch: "main", BranchPrefix: "CIAM-"}
	bc := requestCommit(f.Head("main"))
	bc.Branch, bc.BranchPrefix, bc.DeleteRequestFile = "CIAM-req-1", "CIAM-", false
	return f, h, bc
}

func requestCommits(f *gitlabtest.Fake) int {
	n := 0
	for _, id := range f.CommitOrder {
		if strings.Contains(f.Commits[id].Message, RequestTrailer) {
			n++
		}
	}
	return n
}

func TestHostedCommitSucceedsRootedAtReviewedSHA(t *testing.T) {
	f, h, bc := hostedSetup(t)
	reviewed := bc.ExpectedHead
	// The default branch moves after the movement check, just before the
	// commit: the request must still be rooted at the reviewed SHA.
	f.RaceBeforeCommit = map[string]string{"main.tf": "moved\n"}
	res, err := h.CommitToBranch(context.Background(), bc)
	if err != nil {
		t.Fatal(err)
	}
	if res.MR == nil || res.CommitID == "" {
		t.Fatalf("%+v", res)
	}
	if got := f.Files("CIAM-req-1")["main.tf"]; got != "module \"davinci\" {\n  source = \"./definitions\"\n}\n" {
		t.Fatalf("request branch was not created from the reviewed SHA %s: main.tf = %q", reviewed, got)
	}
}

func TestHostedCommitConflictWhenDefaultBranchMoved(t *testing.T) {
	for name, move := range map[string]func(f *gitlabtest.Fake){
		"before the call": func(f *gitlabtest.Fake) { f.Edit("main", "main.tf", "moved\n") },
		"while the branch is created": func(f *gitlabtest.Fake) {
			f.RaceOnRequest = map[string]map[string]string{"POST /repository/branches": {"main.tf": "moved\n"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, h, bc := hostedSetup(t)
			move(f)
			_, err := h.CommitToBranch(context.Background(), bc)
			var ce *ConflictError
			if !errors.As(err, &ce) || !strings.Contains(err.Error(), "the default branch changed after this request was previewed; preview again") {
				t.Fatalf("want the preview-again conflict, got %v", err)
			}
			if requestCommits(f) != 0 {
				t.Fatal("committed despite the conflict")
			}
			if f.HasBranch("CIAM-req-1") {
				t.Fatal("the empty branch this call created was left behind")
			}
		})
	}
}

func TestHostedCommitUncertainKeepsBranch(t *testing.T) {
	for name, inject := range map[string]func(f *gitlabtest.Fake){
		"5xx after the commit applied": func(f *gitlabtest.Fake) { f.FailAfter = map[string]int{"POST /repository/commits": 502} },
		"5xx before the commit":        func(f *gitlabtest.Fake) { f.Fail["POST /repository/commits"] = 503 },
		"gateway timeout":              func(f *gitlabtest.Fake) { f.Fail["POST /repository/commits"] = 504 },
	} {
		t.Run(name, func(t *testing.T) {
			f, h, bc := hostedSetup(t)
			inject(f)
			_, err := h.CommitToBranch(context.Background(), bc)
			var pe *PartialError
			if !errors.As(err, &pe) || pe.Stage != "commit_uncertain" || pe.Branch != "CIAM-req-1" || !strings.Contains(err.Error(), "CIAM-req-1") {
				t.Fatalf("want an uncertain partial error naming the branch, got %v", err)
			}
			if !f.HasBranch("CIAM-req-1") {
				t.Fatal("an uncertain commit's branch was deleted")
			}
			// Reconciliation: once GitLab answers, resuming either finds the
			// commit or makes it, exactly once.
			f.FailAfter, f.Fail = nil, map[string]int{}
			_, err = h.CommitToBranch(context.Background(), bc)
			var done *AlreadyCommittedError
			if err != nil && !errors.As(err, &done) {
				t.Fatalf("resume: %v", err)
			}
			if requestCommits(f) != 1 {
				t.Fatalf("%d request commits, want 1", requestCommits(f))
			}
		})
	}
}

func TestHostedCommitCertainFailureRemovesOnlyItsOwnBranch(t *testing.T) {
	f, h, bc := hostedSetup(t)
	f.Fail["POST /repository/commits"] = 400
	if _, err := h.CommitToBranch(context.Background(), bc); err == nil {
		t.Fatal("refused commit reported success")
	}
	if f.HasBranch("CIAM-req-1") {
		t.Fatal("a certain refusal left the empty branch this call created")
	}
}

func TestHostedCommitNeverDeletesPreexistingBranch(t *testing.T) {
	for name, inject := range map[string]func(f *gitlabtest.Fake){
		"commit refused":        func(f *gitlabtest.Fake) { f.Fail["POST /repository/commits"] = 400 },
		"default branch moved":  func(f *gitlabtest.Fake) { f.Edit("main", "main.tf", "moved\n") },
		"commit uncertain":      func(f *gitlabtest.Fake) { f.Fail["POST /repository/commits"] = 500 },
		"default branch failed": func(f *gitlabtest.Fake) { f.Fail["GET /repository/branches/main"] = 500 },
	} {
		t.Run(name, func(t *testing.T) {
			f, h, bc := hostedSetup(t)
			f.CreateBranch("CIAM-req-1", "main", nil)
			inject(f)
			if _, err := h.CommitToBranch(context.Background(), bc); err == nil {
				t.Fatal("failure reported success")
			}
			if !f.HasBranch("CIAM-req-1") {
				t.Fatal("a branch that existed before was deleted")
			}
		})
	}
}

func TestHostedCommitBranchNames(t *testing.T) {
	for name, mut := range map[string]func(h *HostedCommitter, b *BranchCommit){
		"other namespace":    func(h *HostedCommitter, b *BranchCommit) { b.Branch = "feature/req-1" },
		"other request":      func(h *HostedCommitter, b *BranchCommit) { b.Branch = "CIAM-req-2" },
		"prefix mismatch":    func(h *HostedCommitter, b *BranchCommit) { b.BranchPrefix = "pingone-promote/" },
		"default prefix":     func(h *HostedCommitter, b *BranchCommit) { b.Branch, b.BranchPrefix = "pingone-promote/req-1", "" },
		"default branch":     func(h *HostedCommitter, b *BranchCommit) { h.DefaultBranch = "CIAM-req-1" },
		"invalid request id": func(h *HostedCommitter, b *BranchCommit) { b.RequestID, b.Branch = "req/../1", "CIAM-req/../1" },
		"invalid prefix": func(h *HostedCommitter, b *BranchCommit) {
			h.BranchPrefix, b.BranchPrefix, b.Branch = "CIAM", "CIAM", "CIAMreq-1"
		},
		"head not a SHA": func(h *HostedCommitter, b *BranchCommit) { b.ExpectedHead = "main" },
		// An abbreviated SHA never equals the full head GitLab reports, so
		// it used to create a branch and then leave it behind.
		"abbreviated head": func(h *HostedCommitter, b *BranchCommit) { b.ExpectedHead = b.ExpectedHead[:12] },
	} {
		t.Run(name, func(t *testing.T) {
			f, h, bc := hostedSetup(t)
			mut(&h, &bc)
			n := len(f.Requests)
			_, err := h.CommitToBranch(context.Background(), bc)
			var pe *PreflightError
			if !errors.As(err, &pe) {
				t.Fatalf("want a preflight refusal, got %v", err)
			}
			if len(f.Requests) != n {
				t.Fatalf("a refused branch name still reached GitLab: %v", f.Requests[n:])
			}
		})
	}
}
