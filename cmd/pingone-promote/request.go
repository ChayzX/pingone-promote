package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"pingone-promote/internal/exporter"
	"pingone-promote/internal/gitlab"
	"pingone-promote/internal/promote"
	"pingone-promote/internal/request"
)

// Configuration read from the environment. Every name can be changed with
// the matching --*-env flag; values are never printed.
var envNames = map[string]string{
	"client-id":       "PINGONE_PROMOTE_WORKER_CLIENT_ID",
	"client-secret":   "PINGONE_PROMOTE_WORKER_CLIENT_SECRET",
	"worker-env":      "PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID",
	"region":          "PINGONE_PROMOTE_REGION_CODE",
	"gitlab-token":    "PINGONE_PROMOTE_GITLAB_TOKEN",
	"tf-path":         "PINGONE_PROMOTE_TERRAFORMER_PATH",
	"tf-sha256":       "PINGONE_PROMOTE_TERRAFORMER_SHA256",
	"tf-version":      "PINGONE_PROMOTE_TERRAFORMER_VERSION",
	"rollout":         "PINGONE_PROMOTE_ROLLOUT",
	"sources":         "PINGONE_PROMOTE_SOURCES",
	"terraform":       "PINGONE_PROMOTE_TERRAFORM_BIN",
	"labels":          "PINGONE_PROMOTE_MR_LABELS",
	"source-env-id":   "PINGONE_PROMOTE_SOURCE_ENVIRONMENT_ID",
	"overlay-pattern": "PINGONE_PROMOTE_OVERLAY_PATTERN",
	"layout":          "PINGONE_PROMOTE_LAYOUT",
}

const requestUsage = `Usage: pingone-promote request [--workspace DIR] [--request FILE | --request-env VAR]
                               [--result FILE] [--dry-run] [--<name>-env VAR ...]

Runs one DaVinci request inside a GitLab CI job on a pingone-promote/<id>
branch (ADR 0017). It reads .pingone-promote/request.json from the
checked-out branch, exports the source environment with the pinned
pingcli-terraformer, merges additively into the branch and, unless the
request (or --dry-run) asks for a preview, commits ONE commit to the same
branch that also deletes the request file, then opens a merge request in
mr mode. It never applies anything.

result.json is always written; the last log line is
"PINGONE_PROMOTE_RESULT {...}". Exit status: 0 previewed, submitted,
no_changes or already_submitted; 1 blocked or failed; 2 usage.

Environment (names configurable with --<name>-env):
  PINGONE_PROMOTE_WORKER_CLIENT_ID / _CLIENT_SECRET / _ENVIRONMENT_ID,
  PINGONE_PROMOTE_REGION_CODE         read-only worker for the source env
  PINGONE_PROMOTE_GITLAB_TOKEN        project access token (Developer, api)
  PINGONE_PROMOTE_TERRAFORMER_PATH / _SHA256 / _VERSION   the pin
  PINGONE_PROMOTE_ROLLOUT             e.g. qa,stage,prod
  PINGONE_PROMOTE_SOURCES             allowed sources, e.g. dev
  PINGONE_PROMOTE_TERRAFORM_BIN       terraform for the fmt gate (or PATH)
  PINGONE_PROMOTE_MR_LABELS           default pingone-promote
  PINGONE_PROMOTE_OVERLAY_PATTERN     default {env}.tfvars
  PINGONE_PROMOTE_LAYOUT              default or terraformer
  CI_SERVER_URL, CI_PROJECT_ID, CI_PROJECT_URL, CI_COMMIT_BRANCH,
  CI_COMMIT_SHA, CI_DEFAULT_BRANCH    set by GitLab
  HTTPS_PROXY, NO_PROXY, SSL_CERT_FILE passed to terraformer when set
`

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type cliEnv struct {
	get func(string) string
}

func (c cliEnv) v(name string) string { return c.get(envNames[name]) }

func registerEnvFlags(fs *flag.FlagSet) {
	for k, def := range envNames {
		k, def := k, def
		fs.Func(k+"-env", "environment variable for "+k+" (default "+def+")", func(s string) error {
			if !regexpEnvName(s) {
				return errors.New("invalid environment variable name")
			}
			envNames[k] = s
			return nil
		})
	}
}

func regexpEnvName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func buildEnv(c cliEnv, workspace string) (request.Env, error) {
	env := request.Env{Workspace: workspace, Rollout: splitList(c.v("rollout")), AllowedSources: splitList(c.v("sources")),
		SourceEnvironmentID: c.v("source-env-id"), TerraformBin: c.v("terraform")}
	l, err := layoutFlag(c.v("layout"))
	if err != nil {
		return env, err
	}
	if p := c.v("overlay-pattern"); p != "" {
		l.OverlayPattern = p
	}
	env.Layout = l
	if labels := c.v("labels"); labels != "" {
		env.MRLabels = splitList(labels)
	}
	env.Credentials = exporter.Credentials{ClientID: c.v("client-id"), ClientSecret: c.v("client-secret"),
		WorkerEnvironmentID: c.v("worker-env"), RegionCode: c.v("region")}
	runner := &exporter.Runner{Config: exporter.Config{Path: c.v("tf-path"), SHA256: c.v("tf-sha256"), Version: c.v("tf-version")},
		Network: exporter.Network{HTTPSProxy: c.get("HTTPS_PROXY"), NoProxy: c.get("NO_PROXY"), CAFile: c.get("SSL_CERT_FILE")}}
	env.Exporter = runner
	env.BaseRepo = func(commit string) (promote.Repo, error) { return gitRepo{dir: workspace, rev: commit}, nil }
	return env, nil
}

func runRequest(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("request", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workspace := fs.String("workspace", ".", "")
	reqFile := fs.String("request", "", "")
	reqEnv := fs.String("request-env", "", "")
	resultPath := fs.String("result", "result.json", "")
	dryRun := fs.Bool("dry-run", false, "")
	registerEnvFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, requestUsage)
			return 0
		}
		fmt.Fprintln(stderr, "pingone-promote request:", err)
		fmt.Fprint(stderr, requestUsage)
		return 2
	}
	c := cliEnv{get: getenv}
	finish := func(res *request.Result) int {
		if err := res.Write(*resultPath); err != nil {
			fmt.Fprintln(stderr, "pingone-promote request: cannot write result:", err)
		}
		fmt.Fprintln(stdout, res.LogLine())
		switch res.Status {
		case request.StatusFailed, request.StatusBlocked:
			return 1
		}
		return 0
	}
	failed := func(code, typ, msg string) int {
		r := &request.Result{ResultVersion: request.ResultVersion, Status: request.StatusFailed, Selected: []request.Selected{}, Changes: []request.ChangeOut{},
			Files: []request.FileOut{}, RequiredSecrets: []request.SecretOut{}, Blockers: map[string][]request.BlockerOut{}, Warnings: []string{},
			Error: &request.ErrorOut{Code: code, Type: typ, Message: msg}}
		return finish(r)
	}
	var raw []byte
	var err error
	switch {
	case *reqFile != "" && *reqEnv != "":
		return failed(request.ErrPreflight, "validation", "use --request or --request-env, not both")
	case *reqEnv != "":
		raw, err = request.DecodeBase64(getenv(*reqEnv))
	default:
		p := *reqFile
		if p == "" {
			p = path.Join(*workspace, gitlab.RequestFile)
		}
		raw, err = readLimited(p, request.MaxSize+1)
	}
	if err != nil {
		return failed(request.ErrPreflight, "validation", err.Error())
	}
	req, err := request.Parse(raw)
	if err != nil {
		return failed(request.ErrPreflight, "validation", err.Error())
	}
	if *dryRun {
		req.DryRun = true
	}
	env, err := buildEnv(c, *workspace)
	if err != nil {
		return failed(request.ErrPreflight, "validation", err.Error())
	}
	env.Branch, env.Head, env.DefaultBranch, env.ProjectURL = getenv("CI_COMMIT_BRANCH"), getenv("CI_COMMIT_SHA"), getenv("CI_DEFAULT_BRANCH"), getenv("CI_PROJECT_URL")
	token := c.v("gitlab-token")
	if token == "" {
		if !req.DryRun {
			return failed(request.ErrPreflight, "validation", envNames["gitlab-token"]+" is not set; a submit needs the project access token")
		}
	} else {
		client, err := gitlab.NewClient(gitlab.Config{BaseURL: getenv("CI_SERVER_URL"), ProjectID: getenv("CI_PROJECT_ID"), TokenEnv: envNames["gitlab-token"],
			TerraformBin: env.TerraformBin}, token)
		if err != nil {
			return failed(request.ErrPreflight, "validation", "GitLab configuration: "+err.Error())
		}
		env.Committer = request.GitLabCommitter{S: &gitlab.Submitter{Client: client, Formatter: env.TerraformBin}}
	}
	if env.Committer == nil {
		// Preview without a token still checks the branch rules.
		env.Committer = previewOnly{}
	}
	env.PriorCommit = func(id string) (string, bool) { return priorCommit(*workspace, id) }
	res := request.Run(ctx, env, req)
	return finish(res)
}

// previewOnly is the committer for a dry run without a GitLab token.
type previewOnly struct{}

func (previewOnly) CommitToBranch(context.Context, gitlab.BranchCommit) (gitlab.Result, error) {
	return gitlab.Result{}, &gitlab.PreflightError{Err: errors.New("no GitLab token; preview only")}
}
func (previewOnly) OpenMergeRequest(context.Context, string) (*gitlab.MergeRequest, error) {
	return nil, nil
}

func readLimited(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("cannot read the request file %s", p)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) >= max {
		return nil, errors.New("request file exceeds the size limit")
	}
	return b, nil
}

// priorCommit finds a commit carrying this request's trailer in the
// checked-out history.
func priorCommit(dir, id string) (string, bool) {
	cmd := exec.Command("git", "-C", dir, "log", "-n", "500", "--format=%H%x1f%B%x1e", "HEAD")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	for _, rec := range bytes.Split(out, []byte{0x1e}) {
		sha, body, ok := bytes.Cut(bytes.TrimSpace(rec), []byte{0x1f})
		if !ok {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(body))
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == gitlab.RequestTrailer+id {
				return string(sha), true
			}
		}
	}
	return "", false
}

// gitRepo reads files at a commit with git (for three-way status).
type gitRepo struct{ dir, rev string }

func (g gitRepo) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	return cmd.Output()
}

func (g gitRepo) Read(p string) ([]byte, bool, error) {
	if strings.HasPrefix(p, "-") || strings.Contains(p, "..") {
		return nil, false, errors.New("unsafe path")
	}
	out, err := g.git("cat-file", "-p", g.rev+":"+p)
	if err != nil {
		return nil, false, nil
	}
	return out, true, nil
}

func (g gitRepo) List(dir string) ([]string, error) {
	spec := g.rev + ":"
	if dir != "." && dir != "" {
		spec += dir
	}
	out, err := g.git("ls-tree", "--name-only", spec)
	if err != nil {
		return nil, nil
	}
	var list []string
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n == "" {
			continue
		}
		list = append(list, path.Join(dir, n))
	}
	return list, nil
}

const exportUsage = `Usage: pingone-promote export --source ENV --flow "NAME or ID" [--repo DIR] [--dry-run]
                              [--result FILE] [--<name>-env VAR ...]

Finds ONE flow in the source environment (case-insensitive name substring
or exact DaVinci ID; an ambiguous query prints the matches and changes
nothing), exports it with its upstream dependencies, and merges it
additively into the repository at --repo (default "."): only the flow's
blocks and new dependencies change; dependencies that differ from Git are
kept and reported. --dry-run prints the diff instead of writing files.
Uses the same environment variables as "pingone-promote request".
`

func runExport(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	source := fs.String("source", "", "")
	flow := fs.String("flow", "", "")
	repo := fs.String("repo", ".", "")
	dry := fs.Bool("dry-run", false, "")
	resultPath := fs.String("result", "", "")
	registerEnvFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, exportUsage)
			return 0
		}
		fmt.Fprintln(stderr, "pingone-promote export:", err)
		fmt.Fprint(stderr, exportUsage)
		return 2
	}
	if *source == "" || strings.TrimSpace(*flow) == "" {
		fmt.Fprint(stderr, "pingone-promote export: --source and --flow are required\n"+exportUsage)
		return 2
	}
	ref := request.FlowRef{Name: *flow}
	if exporter.LooksLikeID(*flow) {
		ref = request.FlowRef{ID: strings.TrimSpace(*flow)}
	}
	req := &request.Request{RequestVersion: request.Version, RequestID: "local-" + time.Now().UTC().Format("20060102-150405"),
		RequestedBy: getenv("USER"), Source: *source, Flows: []request.FlowRef{ref}, Mode: "branch", DryRun: *dry}
	if err := req.Validate(); err != nil {
		fmt.Fprintln(stderr, "pingone-promote export:", err)
		return 2
	}
	env, err := buildEnv(cliEnv{get: getenv}, *repo)
	if err != nil {
		fmt.Fprintln(stderr, "pingone-promote export:", err)
		return 2
	}
	res := request.Run(ctx, env, req)
	if *resultPath != "" {
		_ = res.Write(*resultPath)
	}
	if res.Error != nil {
		fmt.Fprintf(stderr, "pingone-promote export: %s\n", res.Error.Message)
		for _, c := range res.Error.Candidates {
			fmt.Fprintf(stdout, "  %s\t%s\t%s\n", c.Name, c.ID, c.Address)
		}
	}
	for _, c := range res.Changes {
		fmt.Fprintf(stdout, "%-12s %-10s %s\n", c.Action, c.Role, c.Address)
	}
	for env, bs := range res.Blockers {
		for _, b := range bs {
			fmt.Fprintf(stdout, "blocker %s: %s\n", env, b.Message)
		}
	}
	if *dry {
		fmt.Fprint(stdout, res.Diff)
	}
	fmt.Fprintln(stdout, "status:", res.Status)
	if res.Status == request.StatusFailed || res.Status == request.StatusBlocked {
		return 1
	}
	return 0
}
