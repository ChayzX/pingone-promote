package gitlab_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/gitlab/gitlabtest"
	"pingone-promote/internal/tfvars"
)

const env = "environments/prod.tfvars"

func testdata(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newSubmitter(t *testing.T, files map[string]string) (*gitlab.Submitter, *gitlabtest.Fake) {
	t.Helper()
	if _, err := tfvars.ResolveFormatter(""); err != nil {
		t.Skip("terraform or tofu not on PATH; submissions require the fmt gate")
	}
	fake := gitlabtest.New(t, files)
	c, err := gitlab.NewClient(gitlab.Config{BaseURL: fake.BaseURL(), ProjectID: gitlabtest.ProjectID}, gitlabtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = fake.Client()
	return &gitlab.Submitter{Client: c, Paths: gitlab.DefaultPathPolicy}, fake
}

func changeSet(t *testing.T, id string, mode gitlab.Mode) gitlab.ChangeSet {
	return gitlab.ChangeSet{ID: id, Mode: mode, Branch: "promote/" + id, CommitMessage: "Promote Login to prod", Title: "Promote Login",
		Description: "summary", Files: []gitlab.File{
			{Path: env, Content: []byte(testdata(t, "internal/promotelegacy/testdata/e2e_promoted.tfvars"))},
			{Path: "secrets.tf", Content: []byte(testdata(t, "internal/promotelegacy/testdata/e2e_secrets.tf"))},
			{Path: "customer_secrets.yml", Content: []byte(testdata(t, "internal/promotelegacy/testdata/e2e_customer_secrets.yml"))},
		}}
}

func baseFiles(t *testing.T) map[string]string {
	return map[string]string{
		env:                    testdata(t, "internal/promotelegacy/testdata/e2e_target.tfvars"),
		"customer_secrets.yml": testdata(t, "internal/pipelinesecrets/testdata/customer_secrets.yml"),
		".gitlab-ci.yml":       "include: customer_secrets.yml\n",
	}
}

func TestSubmitOneCommitMR(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	_, baseCommit, _ := fake.File("main", env)
	cs := changeSet(t, "s1", gitlab.ModeMR)
	cs.Base = map[string]string{env: baseCommit, "secrets.tf": "", "customer_secrets.yml": baseCommit}
	res, err := s.Submit(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	if res.MR == nil || res.MR.IID != 1 || res.Branch != "promote/s1" {
		t.Fatalf("result %+v", res)
	}
	c := fake.Commits[res.CommitID]
	if len(c.Actions) != 3 || !strings.Contains(c.Message, gitlab.SubmissionTrailer+"s1") {
		t.Fatalf("commit %+v", c)
	}
	for _, a := range c.Actions {
		want := map[string]string{env: "update", "secrets.tf": "create", "customer_secrets.yml": "update"}[a["file_path"]]
		if a["action"] != want {
			t.Fatalf("action %v", a)
		}
		if a["action"] == "update" && a["last_commit_id"] != baseCommit {
			t.Fatal("update without last_commit_id")
		}
	}
	if fake.MRs[0].Description != "summary" || fake.MRs[0].TargetBranch != "main" {
		t.Fatalf("mr %+v", fake.MRs[0])
	}
}

func TestSubmitBranchModeOpensNoMR(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	res, err := s.Submit(context.Background(), changeSet(t, "s2", gitlab.ModeBranch))
	if err != nil {
		t.Fatal(err)
	}
	if res.MR != nil || len(fake.MRs) != 0 || !fake.HasBranch("promote/s2") {
		t.Fatalf("result %+v mrs %d", res, len(fake.MRs))
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	_, baseCommit, _ := fake.File("main", env)
	fake.Edit("main", env, testdata(t, "internal/promotelegacy/testdata/e2e_target.tfvars")+"\n")
	cs := changeSet(t, "s3", gitlab.ModeMR)
	cs.Base = map[string]string{env: baseCommit}
	_, err := s.Submit(context.Background(), cs)
	var conflict *gitlab.ConflictError
	if !errors.As(err, &conflict) || conflict.Paths[0] != env {
		t.Fatalf("want conflict, got %v", err)
	}
	if fake.HasBranch("promote/s3") {
		t.Fatal("conflict left a branch")
	}
	// A secrets.tf that appeared since the draft (base "" = absent) also conflicts.
	fake.Edit("main", "secrets.tf", "# someone else's\n")
	cs = changeSet(t, "s4", gitlab.ModeMR)
	cs.Base = map[string]string{"secrets.tf": ""}
	if _, err := s.Submit(context.Background(), cs); !errors.As(err, &conflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestServerSideConflictIsTyped(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	// The file changes between our read and the commit; GitLab's own
	// last_commit_id check rejects the commit.
	fake.RaceBeforeCommit = map[string]string{env: testdata(t, "internal/promotelegacy/testdata/e2e_target.tfvars") + "\n"}
	_, err := s.Submit(context.Background(), changeSet(t, "s5", gitlab.ModeMR))
	var conflict *gitlab.ConflictError
	if !errors.As(err, &conflict) || fake.HasBranch("promote/s5") {
		t.Fatalf("want conflict and no branch, got %v", err)
	}
	// An unclassified 400 is not a conflict.
	fake.Fail["POST /repository/commits"] = 400
	defer delete(fake.Fail, "POST /repository/commits")
	if _, err := s.Submit(context.Background(), changeSet(t, "s5b", gitlab.ModeMR)); errors.As(err, &conflict) || err == nil {
		t.Fatalf("unclassified 400: %v", err)
	}
}

func TestExistingBranchAndResume(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	// The commit lands, then MR creation fails: a resumable partial.
	fake.Fail["POST /merge_requests"] = 500
	_, err := s.Submit(context.Background(), changeSet(t, "s6", gitlab.ModeMR))
	var partial *gitlab.PartialError
	if !errors.As(err, &partial) || !partial.Resumable || partial.CommitID == "" {
		t.Fatalf("want resumable partial, got %v", err)
	}
	delete(fake.Fail, "POST /merge_requests")
	// Retrying without Resume reports the existing branch before any commit.
	before := len(fake.Commits)
	_, err = s.Submit(context.Background(), changeSet(t, "s6", gitlab.ModeMR))
	if !errors.As(err, &partial) || partial.Stage != "branch_exists" || !partial.Resumable || len(fake.Commits) != before {
		t.Fatalf("want branch_exists partial, got %v", err)
	}
	cs := changeSet(t, "s6", gitlab.ModeMR)
	cs.Resume = true
	res, err := s.Submit(context.Background(), cs)
	if err != nil || !res.Resumed || res.MR == nil || len(fake.Commits) != before {
		t.Fatalf("resume: %+v %v", res, err)
	}
	// Another submission may not reuse the branch.
	other := changeSet(t, "s7", gitlab.ModeMR)
	other.Branch, other.Resume = "promote/s6", true
	if _, err := s.Submit(context.Background(), other); !errors.As(err, &partial) || partial.Resumable {
		t.Fatalf("foreign branch: %v", err)
	}
}

func TestPreflightRefusals(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	var pre *gitlab.PreflightError
	cases := map[string]func(*gitlab.ChangeSet){
		"default branch": func(c *gitlab.ChangeSet) { c.Branch = "main" },
		"bad branch":     func(c *gitlab.ChangeSet) { c.Branch = "bad..name" },
		"lock branch":    func(c *gitlab.ChangeSet) { c.Branch = "x.lock" },
		"ci file":        func(c *gitlab.ChangeSet) { c.Files[0].Path = ".gitlab-ci.yml" },
		"other path":     func(c *gitlab.ChangeSet) { c.Files[0].Path = "terraform/main.tf" },
		"traversal":      func(c *gitlab.ChangeSet) { c.Files[0].Path = "environments/../secrets.tf" },
		"nested env":     func(c *gitlab.ChangeSet) { c.Files[0].Path = "environments/a/b.tfvars" },
		"bad mode":       func(c *gitlab.ChangeSet) { c.Mode = "push" },
		"not tfvars":     func(c *gitlab.ChangeSet) { c.Files[0].Content = []byte("x = var.y\n") },
		"unformatted":    func(c *gitlab.ChangeSet) { c.Files[1].Content = []byte("variable \"a\" {\ntype=string\n}\n") },
		"bad yaml":       func(c *gitlab.ChangeSet) { c.Files[2].Content = []byte("- not a mapping\n") },
		"empty message":  func(c *gitlab.ChangeSet) { c.CommitMessage = "  " },
		"duplicate path": func(c *gitlab.ChangeSet) { c.Files[1] = c.Files[0] },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cs := changeSet(t, "p1", gitlab.ModeMR)
			mutate(&cs)
			if _, err := s.Submit(context.Background(), cs); !errors.As(err, &pre) || !pre.Retryable() {
				t.Fatalf("want preflight error, got %v", err)
			}
		})
	}
	// Protected target branch.
	fake.Edit("main", "README.md", "x")
	cs := changeSet(t, "p2", gitlab.ModeMR)
	cs.Branch = "release"
	s.Client.Do(context.Background(), "POST", "projects/42/repository/branches", map[string]string{"branch": "release", "ref": "main"}, nil)
	fake.Protected["release"] = true
	if _, err := s.Submit(context.Background(), cs); !errors.As(err, &pre) {
		t.Fatalf("protected branch: %v", err)
	}
	// Public project and unprotected default branch.
	fake.Visibility = "public"
	if _, err := s.Submit(context.Background(), changeSet(t, "p3", gitlab.ModeMR)); !errors.As(err, &pre) {
		t.Fatalf("public project: %v", err)
	}
	fake.Visibility, fake.Protected["main"] = "private", false
	if _, err := s.Submit(context.Background(), changeSet(t, "p4", gitlab.ModeMR)); !errors.As(err, &pre) {
		t.Fatalf("unprotected default: %v", err)
	}
	if len(fake.MRs) != 0 {
		t.Fatal("a refused submission wrote to GitLab")
	}
}

func TestMissingFormatterBlocks(t *testing.T) {
	s, _ := newSubmitter(t, baseFiles(t))
	s.Formatter = filepath.Join(t.TempDir(), "no-terraform")
	var pre *gitlab.PreflightError
	var missing *tfvars.FormatterNotFoundError
	_, err := s.Submit(context.Background(), changeSet(t, "f1", gitlab.ModeMR))
	if !errors.As(err, &pre) || !errors.As(err, &missing) {
		t.Fatalf("want preflight with formatter missing, got %v", err)
	}
}

func TestRedirectsAreRefused(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	fake.Redirect["/projects/42"] = "https://evil.example/api/v4/projects/42"
	_, err := s.Submit(context.Background(), changeSet(t, "r1", gitlab.ModeMR))
	var pre *gitlab.PreflightError
	if !errors.As(err, &pre) {
		t.Fatalf("redirect followed or misclassified: %v", err)
	}
}

func TestRevert(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	res, err := s.Submit(context.Background(), changeSet(t, "v1", gitlab.ModeMR))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{env, "customer_secrets.yml", "secrets.tf"}
	// Not merged yet: files on main are older than the commit.
	_, err = s.Revert(context.Background(), gitlab.RevertRequest{ID: "v1r", CommitID: res.CommitID, Paths: paths, Branch: "revert/v1"})
	var conflict *gitlab.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("unmerged revert: %v", err)
	}
	fake.Merge(res.Branch)
	rev, err := s.Revert(context.Background(), gitlab.RevertRequest{ID: "v1r", CommitID: res.CommitID, Paths: paths, Branch: "revert/v1"})
	if err != nil || rev.MR == nil {
		t.Fatalf("revert: %+v %v", rev, err)
	}
	original := testdata(t, "internal/promotelegacy/testdata/e2e_target.tfvars")
	if got, _, _ := fake.File("revert/v1", env); got != original {
		t.Fatal("revert did not restore the tfvars")
	}
	if _, _, ok := fake.File("revert/v1", "secrets.tf"); ok {
		t.Fatal("revert did not remove the created secrets.tf")
	}
	// Changed since: refused unless confirmed; branch mode opens no MR.
	fake.Edit("main", env, original+"\n")
	if _, err := s.Revert(context.Background(), gitlab.RevertRequest{ID: "v1s", CommitID: res.CommitID, Paths: paths, Branch: "revert/v1b", Mode: gitlab.ModeBranch}); !errors.As(err, &conflict) {
		t.Fatalf("changed-since revert: %v", err)
	}
	mrs := len(fake.MRs)
	_, err = s.Revert(context.Background(), gitlab.RevertRequest{ID: "v1s", CommitID: res.CommitID, Paths: paths, Branch: "revert/v1b", Mode: gitlab.ModeBranch, Confirm: true})
	if !errors.As(err, &conflict) && err != nil {
		t.Fatalf("confirmed revert: %v", err)
	}
	if len(fake.MRs) != mrs {
		t.Fatal("branch-mode revert opened an MR")
	}
}

func TestReadsFilesAndTree(t *testing.T) {
	s, fake := newSubmitter(t, baseFiles(t))
	f, err := s.Client.GetFile(context.Background(), "main", env)
	if err != nil || !f.Exists || f.LastCommitID != fake.Head("main") || !strings.Contains(string(f.Content), "tfvars_schema_version") {
		t.Fatalf("file %+v %v", f.Exists, err)
	}
	missing, err := s.Client.GetFile(context.Background(), "main", "environments/none.tfvars")
	if err != nil || missing.Exists {
		t.Fatal("missing file not reported as absent")
	}
	tree, err := s.Client.ListTree(context.Background(), "main", "environments")
	if err != nil || len(tree) != 1 || tree[0].Path != env {
		t.Fatalf("tree %+v %v", tree, err)
	}
	if _, err := s.Client.GetFile(context.Background(), "main", "../etc/passwd"); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestPathPolicy(t *testing.T) {
	p := gitlab.PathPolicy{EnvironmentsDir: "stacks/envs", SecretsTFPath: "stacks/secrets.tf", CustomerSecretsPath: "ci/customer_secrets.yml"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if path, err := p.EnvironmentPath("prod"); err != nil || path != "stacks/envs/prod.tfvars" {
		t.Fatal(path, err)
	}
	for _, bad := range []string{".gitlab-ci.yml", "ci/x.gitlab-ci.yml", "stacks/envs/.hidden.tfvars", "stacks/main.tf", "/abs.tfvars"} {
		if _, err := p.Classify(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if err := (gitlab.PathPolicy{EnvironmentsDir: "e", SecretsTFPath: ".gitlab-ci.yml", CustomerSecretsPath: "c.yml"}).Validate(); err == nil {
		t.Fatal("CI file accepted as secrets.tf path")
	}
}
