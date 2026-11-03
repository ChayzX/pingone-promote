package gitlab

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"pingone-promote/internal/gitlab/gitlabtest"
)

var testPolicy = LayoutPolicy{ModuleDir: "definitions", RootFiles: []string{"main.tf", "variables.tf", "secrets.tf", "imports.tf", "versions.tf"},
	OverlayPattern: "{env}.tfvars", CustomerSecretsPath: "customer_secrets.yml"}

func branchSubmitter(t *testing.T, f *gitlabtest.Fake) *Submitter {
	t.Helper()
	c, err := NewClient(Config{BaseURL: f.BaseURL(), ProjectID: gitlabtest.ProjectID, TokenEnv: "T", TerraformBin: ""}, gitlabtest.Token)
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = f.Client()
	return &Submitter{Client: c, Formatter: requireFormatter(t)}
}

func requireFormatter(t *testing.T) string {
	t.Helper()
	for _, b := range []string{"terraform", "tofu"} {
		if p, err := exec.LookPath(b); err == nil {
			return p
		}
	}
	t.Skip("terraform not on PATH; the fmt gate needs it")
	return ""
}

func requestCommit(head string) BranchCommit {
	return BranchCommit{RequestID: "req-1", Branch: "pingone-promote/req-1", ExpectedHead: head, Mode: ModeMR,
		CommitMessage: "Add Flow B from dev", Title: "PingOne: Flow B", Policy: testPolicy, DeleteRequestFile: true,
		Files: []File{{Path: "definitions/pingone_davinci_flow.tf", Content: []byte("resource \"pingone_davinci_flow\" \"b\" {\n  name = \"B\"\n}\n")},
			{Path: "qa.tfvars", Content: []byte("pingone_environment_id = \"x\"\n")}}}
}

func TestCommitToBranchOnceAndIdempotent(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "module \"davinci\" {\n  source = \"./definitions\"\n}\n"})
	head := f.CreateBranch("pingone-promote/req-1", "main", map[string]string{RequestFile: "{}"})
	s := branchSubmitter(t, f)
	res, err := s.CommitToBranch(context.Background(), requestCommit(head))
	if err != nil {
		t.Fatal(err)
	}
	files := f.Files("pingone-promote/req-1")
	if _, ok := files[RequestFile]; ok {
		t.Fatal("request file must be deleted in the commit")
	}
	if files["qa.tfvars"] == "" || res.MR == nil {
		t.Fatalf("commit or MR missing: %+v", res)
	}
	c := f.Commits[res.CommitID]
	if !strings.Contains(c.Message, RequestTrailer+"req-1") || len(c.Actions) != 3 {
		t.Fatalf("commit: %+v", c)
	}
	n := len(f.CommitOrder)
	// Re-run: the trailer on the head makes it a no-op.
	_, err = s.CommitToBranch(context.Background(), requestCommit(head))
	var done *AlreadyCommittedError
	if !errors.As(err, &done) || done.Result.CommitID != res.CommitID || done.Result.MR == nil {
		t.Fatalf("want AlreadyCommittedError, got %v", err)
	}
	if len(f.CommitOrder) != n || len(f.MRs) != 1 {
		t.Fatal("a re-run must not commit or open another MR")
	}
}

func TestCommitToBranchRefusals(t *testing.T) {
	f := gitlabtest.New(t, map[string]string{"main.tf": "x = 1\n"})
	head := f.CreateBranch("pingone-promote/req-1", "main", map[string]string{RequestFile: "{}"})
	s := branchSubmitter(t, f)
	for name, mut := range map[string]func(*BranchCommit){
		"default branch": func(b *BranchCommit) { b.Branch = "main" },
		"other prefix":   func(b *BranchCommit) { b.Branch = "feature/x" },
		"ci file":        func(b *BranchCommit) { b.Files = []File{{Path: ".gitlab-ci.yml", Content: []byte("x")}} },
		"auto tfvars":    func(b *BranchCommit) { b.Files = []File{{Path: "x.auto.tfvars", Content: []byte("a = 1\n")}} },
		"unlisted root":  func(b *BranchCommit) { b.Files = []File{{Path: "backend.tf", Content: []byte("terraform {}\n")}} },
		"nested defs":    func(b *BranchCommit) { b.Files = []File{{Path: "definitions/x/y.tf", Content: []byte("\n")}} },
	} {
		bc := requestCommit(head)
		mut(&bc)
		if _, err := s.CommitToBranch(context.Background(), bc); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// Somebody pushed to the branch after the job read it.
	f.Edit("pingone-promote/req-1", "notes.md", "x")
	_, err := s.CommitToBranch(context.Background(), requestCommit(head))
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want conflict, got %v", err)
	}
	if len(f.MRs) != 0 {
		t.Fatal("no MR on refusal")
	}
}
