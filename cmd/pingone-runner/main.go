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
	"pingone-promote/internal/operation"
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
	getenv            func(string) string
	gate              func() (runnerGate, error)
	client            func(gitops.Request, func(string) string) (*pingone.Client, error)
	engine            func(*pingone.Client) runnerEngine
	executionAttempts func(string) executionAttemptStore
	pollInterval      time.Duration
	pollTimeout       time.Duration
}

type runnerFailureContext struct {
	command                 string
	draftOutputPath         string
	draftArtifactReady      bool
	draftOutputWriteStarted bool
}

type runnerSafeMessageKind uint8

const (
	runnerValidationMessage runnerSafeMessageKind = iota + 1
	runnerDiagnosticMessage
	runnerReviewMessage
)

type runnerSafeMessage struct {
	kind    runnerSafeMessageKind
	message string
}

func (e *runnerSafeMessage) Error() string { return e.message }

func safeRunnerValidation(message string) error {
	return &runnerSafeMessage{kind: runnerValidationMessage, message: message}
}

func safeRunnerDiagnostic(message string) error {
	return &runnerSafeMessage{kind: runnerDiagnosticMessage, message: message}
}

func safeRunnerReview(message string) error {
	return &runnerSafeMessage{kind: runnerReviewMessage, message: message}
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
			fmt.Fprintln(os.Stderr, cliFailure(err, os.Getenv))
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
		fmt.Fprintln(os.Stderr, cliFailure(err, os.Getenv))
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
	failureContext := runnerFailureContext{}
	if len(args) > 0 {
		failureContext.command = args[0]
	}
	err := runCommand(args, stdout, stderr, deps, &failureContext)
	if err == nil {
		return nil
	}
	var failure *operation.Failure
	if errors.As(err, &failure) {
		return err
	}
	if len(args) == 0 {
		return err
	}
	return classifyRunnerCommandFailure(failureContext, err)
}

func runCommand(args []string, stdout, stderr io.Writer, deps runnerDeps, failureContext *runnerFailureContext) error {
	if len(args) > 0 && args[0] == "--version" {
		if len(args) != 1 {
			return errors.New("--version does not accept additional arguments")
		}
		_, err := fmt.Fprintf(stdout, "pingone-runner %s\n", buildinfo.Version)
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, "usage: pingone-runner <validate|digest|inspect|reconcile-plan|plan|apply|status|rollback-plan> [flags]\n")
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
	if deps.executionAttempts == nil {
		deps.executionAttempts = func(path string) executionAttemptStore { return &executionAttemptFileStore{path: path} }
	}
	if deps.pollInterval <= 0 {
		deps.pollInterval = time.Second
	}
	if deps.pollTimeout <= 0 {
		deps.pollTimeout = 10 * time.Minute
	}
	ctx := context.Background()
	switch args[0] {
	case "reconcile-plan":
		return reconcilePlan(ctx, args[1:], stdout, stderr, deps)
	case "validate":
		fs := flagSet("validate", stderr)
		requestPath := fs.String("request", "", "request JSON file")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *requestPath == "" {
			return safeRunnerValidation("validate requires --request")
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
			return safeRunnerValidation("digest requires --artifact")
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
			return safeRunnerValidation("inspect requires --artifact and --out")
		}
		if samePath(*artifactPath, *outPath) {
			return safeRunnerValidation("diagnostic output must not overwrite the input artifact")
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
			return safeRunnerDiagnostic(err.Error())
		}
		if err = refreshed.Validate(); err != nil {
			return fmt.Errorf("refresh returned an invalid diagnostic artifact: %w", err)
		}
		if err = writePrivateJSON(*outPath, refreshed); err != nil {
			return err
		}
		if len(refreshed.Review.Issues) > 0 {
			return safeRunnerReview("diagnostic artifact written with review issues; inspect did not approve, authorize, or execute; output remains subject to all normal apply gates")
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
		if failureContext != nil {
			failureContext.draftOutputPath = *outPath
		}
		if *requestPath == "" || *outPath == "" {
			return safeRunnerValidation("plan requires --request and --out")
		}
		if samePath(*requestPath, *outPath) || (*digestPath != "" && (samePath(*outPath, *digestPath) || samePath(*requestPath, *digestPath))) || (*summaryPath != "" && (samePath(*summaryPath, *requestPath) || samePath(*summaryPath, *outPath) || (*digestPath != "" && samePath(*summaryPath, *digestPath)))) {
			return safeRunnerValidation("plan outputs must not overwrite an input or each other")
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
		if err := precheckDraftAttempt(*outPath, *requestPath, *outPath, *digestPath, *summaryPath); err != nil {
			return err
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
		a, err := bindDraftAttempts(deps.engine(client), *outPath).Plan(ctx, r, g.Origin())
		if err != nil {
			return err
		}
		if err = a.Validate(); err != nil {
			return fmt.Errorf("native plan returned an invalid artifact: %w", err)
		}
		if failureContext != nil {
			failureContext.draftArtifactReady = true
			failureContext.draftOutputWriteStarted = true
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
				return safeRunnerValidation("digest output must not overwrite an input or artifact")
			}
			if err = writePrivateBytes(*digestPath, []byte(a.Review.Digest+"\n")); err != nil {
				return err
			}
		}
		if len(a.Review.Issues) > 0 {
			return safeRunnerReview("plan artifact written with review issues; deployment is blocked")
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
			return safeRunnerValidation("apply requires --artifact and --out")
		}
		if (*approved == "") == (*approvedFile == "") {
			return safeRunnerValidation("apply requires exactly one of --approved-digest or --approved-digest-file")
		}
		if *approvedFile != "" {
			b, err := readSmallFile(*approvedFile, 256)
			if err != nil {
				return err
			}
			*approved = strings.TrimSpace(string(b))
		}
		if !isDigest(*approved) {
			return safeRunnerValidation("approved digest must be 64 lowercase hexadecimal characters")
		}
		a, err := readArtifact(*artifactPath)
		if err != nil {
			return err
		}
		if samePath(*artifactPath, *outPath) {
			return safeRunnerValidation("result output must not overwrite the input artifact")
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
			return operation.NewFailure("execution_pre_dispatch_refused", "authorization", "Protected GitLab authorization could not be initialized. Verify runner configuration before retrying.", "verify_gitlab_authorization", "", err)
		}
		if err = g.VerifyPlan(ctx, a.Request); err != nil {
			return operation.NewFailure("execution_pre_dispatch_refused", "authorization", "The plan could not be verified against this GitLab job. Use a fresh plan from the trusted project before retrying.", "verify_gitlab_provenance", "", err)
		}
		client, err := deps.client(a.Request, deps.getenv)
		if err != nil {
			return operation.NewFailure("client_setup_failed", "preflight", "The PingOne client could not be prepared. Verify runner credentials and retry before dispatch.", "retry_pre_dispatch", "", err)
		}
		engine := deps.engine(client)
		attempts := deps.executionAttempts(marker)
		markerWritten := false
		result, err := engine.Apply(ctx, a, *approved, func(c context.Context, current gitops.Artifact) error {
			if e := g.Authorize(c, current); e != nil {
				return e
			}
			attempt, e := newExecutionAttempt(a, *approved)
			if e != nil {
				return e
			}
			if e = attempts.Begin(c, attempt); e != nil {
				return e
			}
			markerWritten = true
			return nil
		})
		if err != nil {
			if markerWritten {
				res := executionResult(a, "UNKNOWN", operation.Uncertain, false)
				res.Errors = []string{safeError(err, deps.getenv)}
				_ = writePrivateJSON(*outPath, res)
				return operation.NewFailure("execution_outcome_uncertain", "execution", "PingOne may have received the execution request. The execute intent is retained; reconcile native status before any retry.", "reconcile_native_status", "", err)
			}
			if _, markerErr := os.Lstat(marker); markerErr == nil {
				return operation.NewFailure("execute_intent_checkpoint_failed", "persistence", "Execution was not dispatched, but an execute intent file exists. Inspect it and reconcile operation status before retrying.", "inspect_execute_intent", "", err)
			}
			return operation.NewFailure("execution_pre_dispatch_refused", "preflight", "Execution was not dispatched. Resolve the review, target drift, authorization, or local intent issue before retrying.", "verify_review_and_authorization", "", err)
		}
		if result.ID != a.NativeID {
			failure := errors.New("execute response identity does not match the approved promotion; reconcile native status before any retry")
			res := executionResult(a, "UNKNOWN", operation.Uncertain, false)
			res.Errors = []string{safeError(failure, deps.getenv)}
			_ = writePrivateJSON(*outPath, res)
			return operation.NewFailure("execution_outcome_uncertain", "reconciliation", "The execute response did not establish a matching native operation state. The execute intent is retained; reconcile native status before any retry.", "reconcile_native_status", "", failure)
		}
		attempt, attemptErr := readExecutionAttempt(marker)
		if attemptErr == nil {
			attempt.Dispatch = operation.Identified
			attempt.NativeObserved = true
			attempt.NativeStatus = executionStatusProjection(result.Status)
			attemptErr = attempts.Checkpoint(ctx, attempt)
		}
		if attemptErr != nil {
			return operation.NewFailure("execution_identity_checkpoint_failed", "persistence", "Execution returned a matching native identity, but its status could not be checkpointed. The uncertain execute intent is retained; reconcile native status before any retry.", "reconcile_native_status", "", attemptErr)
		}
		result, pollErr := waitForTerminal(ctx, func(c context.Context) (pingone.Promotion, error) {
			p, statusErr := engine.Status(c, a)
			if statusErr == nil && p.ID != a.NativeID {
				return pingone.Promotion{}, errors.New("native status identity differs from the approved promotion")
			}
			return p, statusErr
		}, result, deps.pollTimeout, deps.pollInterval)
		if result.ID == a.NativeID {
			attempt.NativeStatus = executionStatusProjection(result.Status)
			if attemptErr = attempts.Checkpoint(ctx, attempt); attemptErr != nil {
				return operation.NewFailure("execution_status_checkpoint_failed", "persistence", "Native status was observed, but the execution record could not be updated. The execute intent is retained; reconcile native status before any retry.", "reconcile_native_status", "", attemptErr)
			}
		}
		res := executionResult(a, executionStatusProjection(result.Status), operation.Identified, true)
		res.Errors = pingone.SummarizePromotionErrors(result.Errors, result.PromotionErrors)
		if pollErr != nil {
			res.NeedsReconciliation = true
			res.Errors = append(res.Errors, safeError(pollErr, deps.getenv))
		}
		if err = writePrivateJSON(*outPath, res); err != nil {
			return operation.NewFailure("execution_result_checkpoint_failed", "persistence", "Execution returned, but result output could not be saved. The execute intent is retained; reconcile native status before any retry.", "reconcile_native_status", "", err)
		}
		if pollErr != nil {
			return operation.NewFailure("execution_outcome_uncertain", "reconciliation", "Execution was submitted, but its native status could not be confirmed. The execute intent is retained; reconcile native status before any retry.", "reconcile_native_status", "", pollErr)
		}
		if operation.Classify(result.Status) == operation.Failed {
			return operation.NewFailure("native_execution_failed", "execution", "Native execution failed with an unexpected promotion error and may have partial effects; inspect status and plan a separately approved rollback if needed.", "inspect_status_and_plan_rollback", "", nil)
		}
		return nil
	case "status":
		fs := flagSet("status", stderr)
		artifactPath := fs.String("artifact", "", "artifact JSON file")
		if err := parseFlags(fs, args[1:]); err != nil {
			return err
		}
		if *artifactPath == "" {
			return safeRunnerValidation("status requires --artifact")
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
		if p.ID != a.NativeID {
			return safeRunnerDiagnostic("native status identity differs from the requested promotion")
		}
		out := executionResult(a, executionStatusProjection(p.Status), "", true)
		out.Errors = pingone.SummarizePromotionErrors(p.Errors, p.PromotionErrors)
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
		if failureContext != nil {
			failureContext.draftOutputPath = *outPath
		}
		if *artifactPath == "" || *originalJobID == "" || *outPath == "" {
			return safeRunnerValidation("rollback-plan requires --artifact, --original-job-id and --out")
		}
		if samePath(*artifactPath, *outPath) || (*digestPath != "" && (samePath(*digestPath, *artifactPath) || samePath(*digestPath, *outPath))) || (*summaryPath != "" && (samePath(*summaryPath, *artifactPath) || samePath(*summaryPath, *outPath) || (*digestPath != "" && samePath(*summaryPath, *digestPath)))) {
			return safeRunnerValidation("rollback outputs must not overwrite an input or each other")
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
		if err := precheckDraftAttempt(*outPath, *artifactPath, *outPath, *digestPath, *summaryPath); err != nil {
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
		rollback, err := bindDraftAttempts(deps.engine(client), *outPath).RollbackPlan(ctx, a, g.Origin())
		if err != nil {
			return err
		}
		if err = rollback.Validate(); err != nil {
			return fmt.Errorf("native rollback plan returned invalid artifact: %w", err)
		}
		if failureContext != nil {
			failureContext.draftArtifactReady = true
			failureContext.draftOutputWriteStarted = true
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
				return safeRunnerValidation("digest output must not overwrite an input or artifact")
			}
			if err = writePrivateBytes(*digestPath, []byte(rollback.Review.Digest+"\n")); err != nil {
				return err
			}
		}
		if len(rollback.Review.Issues) > 0 {
			return safeRunnerReview("rollback artifact written with review issues; deployment is blocked")
		}
		return nil
	default:
		return operation.NewFailure("cli_command_invalid", "validation", "Unknown command. Run pingone-runner --help to see supported commands.", "check_runner_usage", "", nil)
	}
}

func classifyRunnerCommandFailure(failureContext runnerFailureContext, cause error) error {
	command := failureContext.command
	var safe *runnerSafeMessage
	if errors.As(cause, &safe) {
		switch safe.kind {
		case runnerValidationMessage:
			return operation.NewFailure("cli_arguments_invalid", "validation", safe.message, "correct_command_arguments", "", nil)
		case runnerDiagnosticMessage:
			switch command {
			case "inspect":
				return operation.NewFailure("diagnostic_identity_mismatch", "validation", safe.message, "verify_source_artifact_identity", "", nil)
			case "status":
				return operation.NewFailure("read_only_status_failed", "reconciliation", safe.message, "verify_requested_native_identity", "", nil)
			case "reconcile-plan":
				return operation.NewFailure("read_only_reconciliation_failed", "reconciliation", safe.message, "inspect_attempt_and_linkage", "", nil)
			}
		case runnerReviewMessage:
			if command == "inspect" {
				return operation.NewFailure("diagnostic_review_blocked", "review", safe.message, "inspect_saved_diagnostic_and_review", "", nil)
			}
			return operation.NewFailure("draft_review_blocked", "review", safe.message, "inspect_saved_artifact_and_review", "", nil)
		}
	}
	switch command {
	case "plan", "rollback-plan":
		out := failureContext.draftOutputPath
		if out != "" {
			if attempt, err := readDraftAttempt(out + ".draft-attempt"); err == nil {
				if attempt.NativeID != "" {
					if failureContext.draftArtifactReady && failureContext.draftOutputWriteStarted {
						return operation.NewFailure("draft_output_persistence_failed", "persistence", "The accepted draft identity is retained in its attempt sidecar, but the artifact or a requested output could not be persisted. Preserve the sidecar and reconcile the plan before continuing.", "reconcile_plan_from_retained_attempt", "", cause)
					}
					return operation.NewFailure("draft_reconciliation_required", "reconciliation", "A draft identity was checkpointed, but planning failed before an artifact was ready to persist. Preserve the attempt sidecar and reconcile the known native identity; do not create another draft.", "reconcile_plan_from_retained_attempt", "", cause)
				}
				return operation.NewFailure("draft_outcome_uncertain", "draft", "A draft attempt record exists without a confirmed native identity. Preserve the sidecar; reconcile the plan or investigate the unknown identity before any new draft.", "reconcile_plan_or_investigate_unknown_identity", "", cause)
			} else if _, statErr := os.Lstat(out + ".draft-attempt"); statErr == nil {
				return operation.NewFailure("draft_outcome_uncertain", "draft", "A draft attempt record exists but its identity cannot be trusted. Preserve the sidecar; reconcile the plan or investigate the unknown identity before any new draft.", "reconcile_plan_or_investigate_unknown_identity", "", cause)
			} else if !os.IsNotExist(statErr) {
				return operation.NewFailure("draft_recovery_evidence_unavailable", "reconciliation", "The runner could not inspect draft recovery evidence. Do not create another draft until the output path and native operation have been investigated.", "investigate_draft_recovery_evidence", "", cause)
			}
		}
		return operation.NewFailure("draft_pre_dispatch_refused", "preflight", "Draft creation was not dispatched. Verify the request, trusted GitLab authorization, and output paths before retrying.", "verify_request_and_gitlab_authorization", "", cause)
	case "status":
		return operation.NewFailure("read_only_status_failed", "reconciliation", "Read-only native status could not be confirmed. Retry the status lookup or investigate the saved artifact and native operation.", "retry_read_only_status_or_investigate", "", cause)
	case "inspect":
		return operation.NewFailure("read_only_inspection_failed", "reconciliation", "Read-only inspection could not be completed. Preserve the source artifact and retry inspection or investigate the native operation.", "retry_read_only_inspection_or_investigate", "", cause)
	case "reconcile-plan":
		return operation.NewFailure("read_only_reconciliation_failed", "reconciliation", "Read-only plan reconciliation could not be completed. Preserve the attempt record and retry reconciliation or investigate the native identity; no new draft was requested.", "retry_reconcile_plan_or_investigate", "", cause)
	default:
		return operation.NewFailure("cli_command_failed", "operation", "The command failed before a safe detail could be classified. Review sanitized runner logs using the correlation ID.", "review_command_and_correlation", "", nil)
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
	Kind                string                  `json:"kind"`
	NativeID            string                  `json:"nativeId"`
	Status              string                  `json:"status"`
	DispatchState       operation.DispatchState `json:"dispatchState,omitempty"`
	NativeObserved      bool                    `json:"nativeObserved,omitempty"`
	EffectsVerified     bool                    `json:"effectsVerified"`
	NeedsReconciliation bool                    `json:"needsReconciliation,omitempty"`
	MayHaveEffects      bool                    `json:"mayHaveEffects,omitempty"`
	UpdatedAt           string                  `json:"updatedAt,omitempty"`
	Errors              []string                `json:"errors,omitempty"`
}

func executionResult(a gitops.Artifact, status string, dispatch operation.DispatchState, observed bool) resultOutput {
	decision := operation.Evaluate(operation.Observation{Status: status, Dispatch: dispatch, EffectsVerified: false})
	return resultOutput{
		Kind: a.Kind, NativeID: a.NativeID, Status: status, DispatchState: dispatch,
		NativeObserved: observed, EffectsVerified: decision.EffectsVerified,
		NeedsReconciliation: decision.NeedsReconciliation, MayHaveEffects: decision.MayHaveEffects,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func waitForTerminal(ctx context.Context, status func(context.Context) (pingone.Promotion, error), current pingone.Promotion, timeout, interval time.Duration) (pingone.Promotion, error) {
	if operation.Classify(current.Status) == operation.Completed || operation.Classify(current.Status) == operation.Failed {
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
			if operation.Classify(current.Status) == operation.Completed || operation.Classify(current.Status) == operation.Failed {
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
		return gitops.Request{}, safeRunnerValidation("request must be selected from requests/<matching-request-id>/request.json")
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
	attempt, err := newExecutionAttempt(a, digest)
	if err != nil {
		return err
	}
	return (&executionAttemptFileStore{path: path}).Begin(context.Background(), attempt)
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
	var failure *operation.Failure
	if errors.As(err, &failure) {
		return failure.Error()
	}
	s := err.Error()
	if strings.Contains(s, "PingOne HTTP ") {
		return "PingOne returned an error. Check native operation status before retrying."
	}
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

func cliFailure(err error, getenv func(string) string) string {
	var failure *operation.Failure
	if !errors.As(err, &failure) {
		failure = operation.NewFailure("cli_failure_unclassified", "operation", "The command failed without a safe classification. Review sanitized runner logs using the correlation ID.", "review_command_and_correlation", "", nil)
	}
	return fmt.Sprintf("%s [code=%s phase=%s recovery=%s correlation=%s]", failure.Error(), failure.Code, failure.Phase, failure.RecoveryAction, failure.CorrelationID)
}
