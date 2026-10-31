// Command pingone-runner provides non-interactive GitLab runner operations for
// reviewed PingOne promotion artifacts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"pingone-promote/internal/buildinfo"
	"pingone-promote/internal/cigate"
	"pingone-promote/internal/gitops"
	"pingone-promote/internal/logging"
	"pingone-promote/internal/pingone"
	"pingone-promote/internal/promotion"
	"pingone-promote/internal/strictjson"
)

const maxArtifactBytes = 16 << 20

type runnerGate interface {
	Origin() gitops.Origin
	VerifyPlan(context.Context, gitops.Request) error
	VerifyOriginalPlan(context.Context, gitops.Artifact, string) error
	Authorize(context.Context, gitops.Artifact) error
}

type runnerEngine interface {
	Plan(context.Context, gitops.Request, gitops.Origin) (gitops.Artifact, error)
	Refresh(context.Context, gitops.Artifact) (gitops.Artifact, error)
	Apply(context.Context, gitops.Artifact, string, func(context.Context, gitops.Artifact) error) (pingone.Promotion, error)
	Status(context.Context, gitops.Artifact) (pingone.Promotion, error)
	RollbackPlan(context.Context, gitops.Artifact, gitops.Origin) (gitops.Artifact, error)
}

type runnerDeps struct {
	getenv       func(string) string
	gate         func() (runnerGate, error)
	client       func(gitops.Request, func(string) string) (*pingone.Client, error)
	engine       func(*pingone.Client) runnerEngine
	pollInterval time.Duration
	pollTimeout  time.Duration
}

func defaultDeps(getenv func(string) string) runnerDeps {
	return runnerDeps{
		getenv: getenv,
		gate: func() (runnerGate, error) {
			cfg, err := cigate.FromEnv(getenv)
			if err != nil {
				return nil, err
			}
			return &cigate.Verifier{Config: cfg}, nil
		},
		client:       makePingOneClient,
		engine:       func(c *pingone.Client) runnerEngine { return &gitops.Engine{Client: c} },
		pollInterval: time.Second,
		pollTimeout:  10 * time.Minute,
	}
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && (args[0] == "--version" || args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		if err := run(args, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, safeError(err, os.Getenv))
			os.Exit(1)
		}
		return
	}
	logger, closeLog, err := logging.Open(logging.Config{
		Level: os.Getenv("PINGONE_PROMOTE_LOG_LEVEL"), Format: os.Getenv("PINGONE_PROMOTE_LOG_FORMAT"), File: os.Getenv("PINGONE_PROMOTE_LOG_FILE"),
	}, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pingone-runner: could not initialize structured logging")
		os.Exit(1)
	}
	if err := run(args, os.Stdout, os.Stderr); err != nil {
		logger.Event(context.Background(), slog.LevelError, "cli.failure", "command", "", "", "", "command_failed", 0, 0)
		fmt.Fprintln(os.Stderr, safeError(err, os.Getenv))
		_ = closeLog()
		os.Exit(1)
	}
	if err := closeLog(); err != nil {
		fmt.Fprintln(os.Stderr, "pingone-runner: could not close structured log output")
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	return runWith(args, stdout, stderr, defaultDeps(os.Getenv))
}

func runWith(args []string, stdout, stderr io.Writer, deps runnerDeps) error {
	if len(args) > 0 && args[0] == "--version" {
		if len(args) != 1 {
			return errors.New("--version does not accept additional arguments")
		}
		_, err := fmt.Fprintf(stdout, "pingone-runner %s\n", buildinfo.Version)
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, "usage: pingone-runner <validate|digest|inspect|plan|apply|status|rollback-plan> [flags]\n")
		return nil
	}
	if deps.getenv == nil {
		deps.getenv = func(string) string { return "" }
	}
	if deps.gate == nil {
		deps.gate = func() (runnerGate, error) { return nil, errors.New("runner CI verifier is unavailable") }
	}
	if deps.client == nil {
		deps.client = func(gitops.Request, func(string) string) (*pingone.Client, error) {
			return nil, errors.New("PingOne client factory is unavailable")
		}
	}
	if deps.engine == nil {
		deps.engine = func(c *pingone.Client) runnerEngine { return &gitops.Engine{Client: c} }
	}
	if deps.pollInterval <= 0 {
		deps.pollInterval = time.Second
	}
	if deps.pollTimeout <= 0 {
		deps.pollTimeout = 10 * time.Minute
	}
	ctx := context.Background()
	switch args[0] {
	case "validate":
		fs := flagSet("validate", stderr)
		requestPath := fs.String("request", "", "request JSON file")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *requestPath == "" {
			return errors.New("validate requires --request")
		}
		if _, err := readRequest(*requestPath); err != nil {
			return err
		}
		_, err := io.WriteString(stdout, "valid\n")
		return err
	case "digest":
		fs := flagSet("digest", stderr)
		artifactPath := fs.String("artifact", "", "artifact JSON file")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *artifactPath == "" {
			return errors.New("digest requires --artifact")
		}
		a, err := readArtifact(*artifactPath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, a.Review.Digest)
		return err
	case "inspect":
		fs := flagSet("inspect", stderr)
		artifactPath := fs.String("artifact", "", "existing artifact JSON file")
		outPath := fs.String("out", "", "fresh diagnostic artifact output path")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *artifactPath == "" || *outPath == "" {
			return errors.New("inspect requires --artifact and --out")
		}
		if samePath(*artifactPath, *outPath) {
			return errors.New("diagnostic output must not overwrite the input artifact")
		}
		if err := requireAbsent(*outPath); err != nil {
			return err
		}
		a, err := readArtifact(*artifactPath)
		if err != nil {
			return err
		}
		g, err := deps.gate()
		if err != nil {
			return err
		}
		if !sameTrustedProject(a.Origin, g.Origin()) {
			return errors.New("artifact origin does not match the trusted GitLab server and project")
		}
		if err = g.VerifyPlan(ctx, a.Request); err != nil {
			return err
		}
		client, err := deps.client(a.Request, deps.getenv)
		if err != nil {
			return err
		}
		engine := deps.engine(client)
		refreshed, err := engine.Refresh(ctx, a)
		if err != nil {
			return err
		}
		if err = validateRefreshIdentity(a, refreshed); err != nil {
			return err
		}
		if err = refreshed.Validate(); err != nil {
			return fmt.Errorf("refresh returned an invalid diagnostic artifact: %w", err)
		}
		if err = writePrivateJSON(*outPath, refreshed); err != nil {
			return err
		}
		if len(refreshed.Review.Issues) > 0 {
			return errors.New("diagnostic artifact written with review issues; inspect did not approve, authorize, or execute; output remains subject to all normal apply gates")
		}
		_, err = io.WriteString(stdout, "diagnostic inspection complete; no review issues; inspect did not approve, authorize, or execute; output remains subject to all normal apply gates\n")
		return err
	case "plan":
		fs := flagSet("plan", stderr)
		requestPath := fs.String("request", "", "request JSON file")
		outPath := fs.String("out", "", "artifact output path")
		digestPath := fs.String("digest-out", "", "optional digest output path")
		summaryPath := fs.String("summary-out", "", "optional Markdown change summary output path")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *requestPath == "" || *outPath == "" {
			return errors.New("plan requires --request and --out")
		}
		if samePath(*requestPath, *outPath) || (*digestPath != "" && (samePath(*outPath, *digestPath) || samePath(*requestPath, *digestPath))) || (*summaryPath != "" && (samePath(*summaryPath, *requestPath) || samePath(*summaryPath, *outPath) || (*digestPath != "" && samePath(*summaryPath, *digestPath)))) {
			return errors.New("plan outputs must not overwrite an input or each other")
		}
		if err := requireAbsent(*outPath); err != nil {
			return err
		}
		if *digestPath != "" {
			if err := requireAbsent(*digestPath); err != nil {
				return err
			}
		}
		if *summaryPath != "" {
			if err := requireAbsent(*summaryPath); err != nil {
				return err
			}
		}
		r, err := readRequest(*requestPath)
		if err != nil {
			return err
		}
		g, err := deps.gate()
		if err != nil {
			return err
		}
		if err = g.VerifyPlan(ctx, r); err != nil {
			return err
		}
		client, err := deps.client(r, deps.getenv)
		if err != nil {
			return err
		}
		a, err := deps.engine(client).Plan(ctx, r, g.Origin())
		if err != nil {
			return err
		}
		if err = a.Validate(); err != nil {
			return fmt.Errorf("native plan returned an invalid artifact: %w", err)
		}
		if err = writePrivateJSON(*outPath, a); err != nil {
			return err
		}
		if *summaryPath != "" {
			if err = writePrivateBytes(*summaryPath, []byte(promotion.RenderMarkdown(a.Review))); err != nil {
				return err
			}
		}
		if *digestPath != "" {
			if samePath(*outPath, *digestPath) || samePath(*requestPath, *digestPath) {
				return errors.New("digest output must not overwrite an input or artifact")
			}
			if err = writePrivateBytes(*digestPath, []byte(a.Review.Digest+"\n")); err != nil {
				return err
			}
		}
		if len(a.Review.Issues) > 0 {
			return errors.New("plan artifact written with review issues; deployment is blocked")
		}
		return nil
	case "apply":
		fs := flagSet("apply", stderr)
		artifactPath := fs.String("artifact", "", "artifact JSON file")
		approved := fs.String("approved-digest", "", "review digest approved by deployment")
		approvedFile := fs.String("approved-digest-file", "", "file containing approved review digest")
		outPath := fs.String("out", "", "result JSON output path")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *artifactPath == "" || *outPath == "" {
			return errors.New("apply requires --artifact and --out")
		}
		if (*approved == "") == (*approvedFile == "") {
			return errors.New("apply requires exactly one of --approved-digest or --approved-digest-file")
		}
		if *approvedFile != "" {
			b, err := readSmallFile(*approvedFile, 256)
			if err != nil {
				return err
			}
			*approved = strings.TrimSpace(string(b))
		}
		if !isDigest(*approved) {
			return errors.New("approved digest must be 64 lowercase hexadecimal characters")
		}
		a, err := readArtifact(*artifactPath)
		if err != nil {
			return err
		}
		if samePath(*artifactPath, *outPath) {
			return errors.New("result output must not overwrite the input artifact")
		}
		if err := requireAbsent(*outPath); err != nil {
			return err
		}
		marker := *artifactPath + ".execute-intent"
		if _, err = os.Lstat(marker); err == nil {
			return errors.New("execute intent marker already exists; reconcile native status before any retry")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect execute intent marker: %w", err)
		}
		g, err := deps.gate()
		if err != nil {
			return err
		}
		if err = g.VerifyPlan(ctx, a.Request); err != nil {
			return err
		}
		client, err := deps.client(a.Request, deps.getenv)
		if err != nil {
			return err
		}
		engine := deps.engine(client)
		markerWritten := false
		result, err := engine.Apply(ctx, a, *approved, func(c context.Context, current gitops.Artifact) error {
			if e := g.Authorize(c, current); e != nil {
				return e
			}
			if e := createIntent(marker, a, *approved); e != nil {
				return e
			}
			markerWritten = true
			return nil
		})
		if err != nil {
			if markerWritten {
				res := resultOutput{Kind: a.Kind, NativeID: a.NativeID, Status: "IN_PROGRESS", Errors: []string{safeError(err, deps.getenv)}, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
				_ = writePrivateJSON(*outPath, res)
				return fmt.Errorf("%s; execute intent retained at %s; reconcile native status before any retry", safeError(err, deps.getenv), marker)
			}
			return err
		}
		result, pollErr := waitForTerminal(ctx, func(c context.Context) (pingone.Promotion, error) { return engine.Status(c, a) }, result, deps.pollTimeout, deps.pollInterval)
		res := resultOutput{Kind: a.Kind, NativeID: a.NativeID, Status: result.Status, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Errors: redactNativeErrors(result.Errors, result.PromotionErrors)}
		if pollErr != nil {
			res.Status = "IN_PROGRESS"
			res.Errors = append(res.Errors, safeError(pollErr, deps.getenv))
		}
		if err = writePrivateJSON(*outPath, res); err != nil {
			return fmt.Errorf("execution returned; result output failed, intent retained at %s; reconcile native status before any retry", marker)
		}
		if pollErr != nil {
			return fmt.Errorf("execution status is uncertain; intent retained at %s; reconcile native status before any retry: %s", marker, safeError(pollErr, deps.getenv))
		}
		if result.Status == "ERROR" {
			detail := strings.Join(res.Errors, "; ")
			if detail != "" {
				detail = ": " + detail
			}
			return fmt.Errorf("native execution ended in ERROR%s; intent retained at %s; inspect status and plan rollback if needed", detail, marker)
		}
		return nil
	case "status":
		fs := flagSet("status", stderr)
		artifactPath := fs.String("artifact", "", "artifact JSON file")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *artifactPath == "" {
			return errors.New("status requires --artifact")
		}
		a, err := readArtifact(*artifactPath)
		if err != nil {
			return err
		}
		g, err := deps.gate()
		if err != nil {
			return err
		}
		if err = g.VerifyPlan(ctx, a.Request); err != nil {
			return err
		}
		client, err := deps.client(a.Request, deps.getenv)
		if err != nil {
			return err
		}
		p, err := deps.engine(client).Status(ctx, a)
		if err != nil {
			return err
		}
		out := resultOutput{Kind: a.Kind, NativeID: a.NativeID, Status: p.Status, Errors: redactNativeErrors(p.Errors, p.PromotionErrors)}
		return json.NewEncoder(stdout).Encode(out)
	case "rollback-plan":
		fs := flagSet("rollback-plan", stderr)
		artifactPath := fs.String("artifact", "", "forward artifact JSON file")
		originalJobID := fs.String("original-job-id", "", "original plan_promotion job ID")
		outPath := fs.String("out", "", "rollback artifact output path")
		digestPath := fs.String("digest-out", "", "optional digest output path")
		summaryPath := fs.String("summary-out", "", "optional Markdown change summary output path")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *artifactPath == "" || *originalJobID == "" || *outPath == "" {
			return errors.New("rollback-plan requires --artifact, --original-job-id and --out")
		}
		if samePath(*artifactPath, *outPath) || (*digestPath != "" && (samePath(*digestPath, *artifactPath) || samePath(*digestPath, *outPath))) || (*summaryPath != "" && (samePath(*summaryPath, *artifactPath) || samePath(*summaryPath, *outPath) || (*digestPath != "" && samePath(*summaryPath, *digestPath)))) {
			return errors.New("rollback outputs must not overwrite an input or each other")
		}
		if err := requireAbsent(*outPath); err != nil {
			return err
		}
		if *digestPath != "" {
			if err := requireAbsent(*digestPath); err != nil {
				return err
			}
		}
		if *summaryPath != "" {
			if err := requireAbsent(*summaryPath); err != nil {
				return err
			}
		}
		a, err := readArtifact(*artifactPath)
		if err != nil {
			return err
		}
		g, err := deps.gate()
		if err != nil {
			return err
		}
		if err = g.VerifyPlan(ctx, a.Request); err != nil {
			return err
		}
		if err = g.VerifyOriginalPlan(ctx, a, *originalJobID); err != nil {
			return err
		}
		client, err := deps.client(a.Request, deps.getenv)
		if err != nil {
			return err
		}
		rollback, err := deps.engine(client).RollbackPlan(ctx, a, g.Origin())
		if err != nil {
			return err
		}
		if err = rollback.Validate(); err != nil {
			return fmt.Errorf("native rollback plan returned invalid artifact: %w", err)
		}
		if err = writePrivateJSON(*outPath, rollback); err != nil {
			return err
		}
		if *summaryPath != "" {
			if err = writePrivateBytes(*summaryPath, []byte(promotion.RenderMarkdown(rollback.Review))); err != nil {
				return err
			}
		}
		if *digestPath != "" {
			if samePath(*digestPath, *artifactPath) || samePath(*digestPath, *outPath) {
				return errors.New("digest output must not overwrite an input or artifact")
			}
			if err = writePrivateBytes(*digestPath, []byte(rollback.Review.Digest+"\n")); err != nil {
				return err
			}
		}
		if len(rollback.Review.Issues) > 0 {
			return errors.New("rollback artifact written with review issues; deployment is blocked")
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func makePingOneClient(r gitops.Request, getenv func(string) string) (*pingone.Client, error) {
	profile := pingone.Profile{Region: r.Source.Region, EnvironmentID: r.Source.ID, AuthEnvironmentID: getenv("PINGONE_AUTH_ENVIRONMENT_ID"), ClientID: getenv("PINGONE_CLIENT_ID"), SecretMode: getenv("PINGONE_SECRET_MODE"), SecretEnv: "PINGONE_CLIENT_SECRET"}
	if profile.ClientID == "" {
		return nil, errors.New("PINGONE_CLIENT_ID is required")
	}
	return pingone.NewClient(profile, getenv("PINGONE_CLIENT_SECRET"))
}

type resultOutput struct {
	Kind      string   `json:"kind"`
	NativeID  string   `json:"nativeId"`
	Status    string   `json:"status"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

func waitForTerminal(ctx context.Context, status func(context.Context) (pingone.Promotion, error), current pingone.Promotion, timeout, interval time.Duration) (pingone.Promotion, error) {
	if current.Status == "COMPLETED" || current.Status == "ERROR" {
		return current, nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	if interval <= 0 {
		interval = time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return current, fmt.Errorf("native execution did not reach a terminal status: %w", ctx.Err())
		case <-ticker.C:
			p, err := status(ctx)
			if err != nil {
				return current, fmt.Errorf("could not confirm native execution status: %w", err)
			}
			current = p
			if current.Status == "COMPLETED" || current.Status == "ERROR" {
				return current, nil
			}
		}
	}
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("output already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect output path: %w", err)
	}
	return nil
}

func flagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func readRequest(path string) (gitops.Request, error) {
	f, err := os.Open(path)
	if err != nil {
		return gitops.Request{}, fmt.Errorf("could not open request: %w", err)
	}
	defer f.Close()
	r, err := gitops.Decode(f)
	if err != nil {
		return r, err
	}
	dir := filepath.Dir(filepath.Clean(path))
	if filepath.Base(path) != "request.json" || filepath.Base(dir) != r.ID || filepath.Base(filepath.Dir(dir)) != "requests" {
		return gitops.Request{}, errors.New("request must be selected from requests/<matching-request-id>/request.json")
	}
	return r, nil
}
func readArtifact(path string) (gitops.Artifact, error) {
	b, err := readSmallFile(path, maxArtifactBytes)
	if err != nil {
		return gitops.Artifact{}, fmt.Errorf("could not read artifact: %w", err)
	}
	var a gitops.Artifact
	if err = strictjson.Unmarshal(b, &a); err != nil {
		return a, errors.New("invalid artifact JSON or unknown fields")
	}
	if err = a.Validate(); err != nil {
		return a, fmt.Errorf("invalid artifact: %w", err)
	}
	return a, nil
}
func readSmallFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return b, nil
}
func writePrivateJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("could not encode output JSON")
	}
	return writePrivateBytes(path, append(b, '\n'))
}
func writePrivateBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pingone-runner-*")
	if err != nil {
		return fmt.Errorf("could not create private output: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("could not write private output: %w", err)
	}
	if err = os.Link(tmpPath, path); err != nil {
		return fmt.Errorf("could not create output without overwriting an existing file: %w", err)
	}
	return nil
}
func createIntent(path string, a gitops.Artifact, digest string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("could not create execute intent marker: %w", err)
	}
	defer f.Close()
	data := []byte(fmt.Sprintf("artifact=%s\napprovedDigest=%s\ncreatedAt=%s\n", a.NativeID, digest, time.Now().UTC().Format(time.RFC3339Nano)))
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	return err
}
func samePath(a, b string) bool {
	aa, e1 := filepath.Abs(a)
	bb, e2 := filepath.Abs(b)
	return e1 == nil && e2 == nil && aa == bb
}

func sameTrustedProject(artifact, trusted gitops.Origin) bool {
	artifactURL, artifactErr := url.Parse(artifact.ServerURL)
	trustedURL, trustedErr := url.Parse(trusted.ServerURL)
	if artifactErr != nil || trustedErr != nil || artifact.ProjectID != trusted.ProjectID {
		return false
	}
	return strings.EqualFold(artifactURL.Scheme, trustedURL.Scheme) && strings.EqualFold(artifactURL.Host, trustedURL.Host) && strings.TrimSuffix(artifactURL.Path, "/") == strings.TrimSuffix(trustedURL.Path, "/")
}

func validateRefreshIdentity(original, refreshed gitops.Artifact) error {
	if original.Version != refreshed.Version || original.Kind != refreshed.Kind || original.NativeID != refreshed.NativeID || original.OriginalNativeID != refreshed.OriginalNativeID || original.CreatedAt != refreshed.CreatedAt || original.TargetSnapshotID != refreshed.TargetSnapshotID || original.Origin != refreshed.Origin || !reflect.DeepEqual(original.Request, refreshed.Request) {
		return errors.New("refresh changed the original artifact identity or provenance")
	}
	return nil
}
func isDigest(s string) bool { return regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(s) }

var credentialPattern = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]+=*|((?:client[_ -]?secret|password|access[_ -]?token|api[_ -]?key)["'=: ]+)[^\s,;"']+`)

func safeError(err error, getenv func(string) string) string {
	s := err.Error()
	for _, name := range []string{"PINGONE_CLIENT_SECRET", "CI_JOB_TOKEN", "GITLAB_READ_API_TOKEN"} {
		if v := getenv(name); v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	s = credentialPattern.ReplaceAllString(s, "$1[redacted]")
	if len(s) > 1000 {
		s = s[:1000]
	}
	return s
}
func redactNativeErrors(legacy, promotionErrors json.RawMessage) []string {
	if !hasNativeErrors(legacy) && !hasNativeErrors(promotionErrors) {
		return nil
	}
	var entries []string
	for _, raw := range []json.RawMessage{legacy, promotionErrors} {
		if !hasNativeErrors(raw) {
			continue
		}
		var value any
		if json.Unmarshal(raw, &value) != nil {
			entries = append(entries, "PingOne reported a promotion error; inspect status in PingOne.")
			continue
		}
		switch values := value.(type) {
		case []any:
			for _, value := range values {
				entries = append(entries, summarizeNativeError(value))
			}
		default:
			entries = append(entries, summarizeNativeError(value))
		}
	}
	seen := map[string]bool{}
	result := make([]string, 0, min(len(entries), 5))
	for _, entry := range entries {
		if seen[entry] {
			continue
		}
		seen[entry] = true
		result = append(result, entry)
		if len(result) == 5 {
			break
		}
	}
	return result
}

func hasNativeErrors(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	switch value := value.(type) {
	case nil:
		return false
	case []any:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	case string:
		return value != ""
	default:
		return true
	}
}

func summarizeNativeError(value any) string {
	const generic = "PingOne reported a promotion error; inspect status in PingOne."
	var raw string
	switch value := value.(type) {
	case string:
		raw = value
	case map[string]any:
		if code, ok := value["code"].(string); ok {
			return summarizeNativeCode(code)
		}
		return generic
	default:
		return generic
	}
	// PingOne may wrap a JSON object in an HTTP status prefix. Decode only the
	// final object and use its code as a category; never relay message or IDs.
	start := strings.Index(raw, "{")
	if start < 0 {
		return generic
	}
	var detail struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(strings.NewReader(raw[start:])).Decode(&detail) != nil {
		return generic
	}
	return summarizeNativeCode(detail.Code)
}

func summarizeNativeCode(code string) string {
	switch code {
	case "UNEXPECTED_ERROR":
		return "PingOne reported an unexpected promotion error; inspect status in PingOne."
	default:
		return "PingOne reported a promotion error; inspect status in PingOne."
	}
}
