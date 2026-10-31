# Recoverable Operation Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Preserve mutation recovery handles and expose safe next actions without replaying uncertain writes.
**Architecture:** Pure lifecycle classification is shared by adapters; effectful orchestration checkpoints attempts before dispatch and identity before observation. Reconciliation is read-only and retains all existing provenance and approval checks.
**Tech Stack:** Go, existing private files, GitLab artifacts, existing browser UI.
**Spec:** docs/superpowers/specs/2026-10-31-senior-quality-rework-design.md

## Global Constraints

- Production planning, deployment and rollback stay GitLab-only; preserve authorization, provenance, digest, drift and no-replay boundaries.
- DaVinci is a separate setup/build: NEVER merge main into it.
- No live tenant data, credentials or raw evidence in public source. Do not read or stage internal/app/live_identity_test.go.
- macOS/Linux product support; Windows is a development and browser host.
- Keep legacy artifacts readable. Additive metadata does not change existing approval semantics. Reconstructed reviews require normal digest and approval gates.
- No exactly-once guarantee across lost ephemeral runners or independent pipelines.

## Review Focus

- Unrecognized or differently cased native status must never become executable.
- Accepted POST followed by checkpoint, polling or output failure must never permit an automatic second POST.
- Missing or conflicting recovery evidence must block mutation rather than select a guessed native identity.
- Existing GitLab content with the expected branch name but different request content must never be overwritten.
- Raw vendor fields and credentials must not enter operator failures, progress or attempt records.

### Task 1: Pure lifecycle authority

**Files:** Create internal/operation/lifecycle.go and lifecycle_test.go. No adapters changed yet.
**Interfaces:** Produce Classify(status string) NativeState; Evaluate(Observation) Decision.
NativeState constants: Unknown, Preparing, Ready, Running, Completed, Failed, RolledBack, Invalid.
Observation fields: Status string, Dispatch DispatchState, EffectsVerified bool.
DispatchState constants: NotDispatched, Uncertain, Identified. Decision fields: Native NativeState, CanExecute bool, NeedsReconciliation bool, MayHaveEffects bool, EffectsVerified bool.
- [ ] Write table-driven tests: preparation states NEW/PREPARING/ADDITIONAL_SNAPSHOTS_REQUESTED/ROLLBACK_NEW/ROLLBACK_PREPARING; READY; IN_PROGRESS/ROLLBACK_IN_PROGRESS; COMPLETED; ERROR; ROLLED_BACK; VALIDATION_FAILED. Empty, lowercase ready and future status classify Unknown.
- [ ] Verify tests fail before implementation.
- [ ] Implement pure classification. CanExecute only exact READY with NotDispatched; actual authorization/review predicates remain in adapters. Uncertain dispatch requires reconciliation. Identified READY requires reconciliation. Unknown requires reconciliation. Running/Completed/Failed/RolledBack may have effects regardless of dispatch. Uncertain/Identified may have effects even with READY. EffectsVerified is independent; never inferred from native completion.
- [ ] Verify tests pass; commit only these two files.

### Task 2: Draft attempt checkpoint tracer slice

**Files:** Create internal/gitops/attempt.go and attempt_test.go; modify internal/gitops/engine.go, engine_test.go; create cmd/pingone-runner/attempt.go and attempt_test.go; modify main.go/main_test.go; update docs/gitlab.md and pipeline artifact paths.
**Interfaces:** Engine gains an optional DraftAttempts AttemptStore dependency. AttemptStore exposes Begin(context.Context, DraftAttempt) error and Checkpoint(context.Context, DraftAttempt) error. DraftAttempt is versioned metadata containing kind, request fingerprint, trusted origin, native ID, original native ID, source/target IDs and snapshot IDs, creation time; no Request, configurations or secrets. A runner store owns a single deterministic sidecar at out path + .draft-attempt. Begin uses exclusive create; Checkpoint atomically replaces the same bound record. No automatic resume through Plan.
- [ ] Write engine fault-injection tests asserting Begin occurs after validation but before POST; Begin failure sends zero POSTs; accepted identity is checkpointed before first poll; checkpoint failure returns partial identity and sends no poll; POST failure leaves intent.
- [ ] Run tests red. Implement forward and rollback checkpoints immediately around POST. Retain safe partial identity on all later errors; validate rollback identity and previous linkage before accepting checkpoint as trusted.
- [ ] Write runner restart/output tests: same sidecar blocks a second Plan/RollbackPlan, malformed record fails closed, concurrent exclusive create yields at most one POST, no metadata payloads, output failure preserves checkpoint.
- [ ] Run red; wire default runner engine store for plan and rollback-plan without requiring alternate fake engines to persist. Reject sidecar/input/output aliasing before writes. Keep existing flags; new sidecar is retained on success and failure.
- [ ] Run focused Go tests green. Document always-upload sidecar and loss-before-artifact-upload limitation. Commit explicit files only.

### Task 3: Read-only draft reconciliation

**Files:** internal/gitops/attempt.go, engine.go and tests; cmd/pingone-runner/attempt.go, main.go and tests; docs/gitlab.md.
**Interfaces:** Engine.ReconcileDraft(ctx context.Context, attempt DraftAttempt, request Request, origin Origin) (Artifact,error). Runner reconcile-plan accepts --attempt, --request, --out; rollback additionally original artifact and original job identity using existing VerifyOriginalPlan gate.
- [ ] Test zero POSTs on every reconciliation path; mismatched request, origin, source/target/snapshots, unknown ID and original linkage fail closed.
- [ ] Implement reads with fresh VerifyPlan and rollback provenance, pinned inventories and native identity validation, then normal review/digest reconstruction. Write a new output only; do not modify original evidence. Reconciled output cannot bypass apply gates.
- [ ] Test rejected legacy execution where semantics differ; otherwise preserve artifact version 1 and existing validation. Test lost-ID guidance never guesses candidates.
- [ ] Run targeted tests; document exact recovery commands and commit.

### Task 4: Integrate lifecycle and failure contract

**Files:** internal/operation/failure.go and tests; internal/gitops/engine.go; internal/app/workflow.go/server.go; cmd/pingone-runner/main.go; affected tests and web assets.
**Interfaces:** Failure fields Code, Phase, Message, RecoveryAction, CorrelationID; Error() returns safe Message. Add HTTP fields while retaining error text. Classify adapter failures without including raw vendor text. Preserve causes only for controlled sanitized diagnostics.
- [ ] Characterize existing blocked apply (no dispatch), uncertain execute, native partial failure and unknown status. Assert mutation counts, safe actions and no leaked arbitrary vendor fields.
- [ ] Use Task 1 classification for shared status predicates; retain distinct local-target and GitLab authorization policies. Never infer effects verification from status.
- [ ] Test additive HTTP compatibility and CLI safe text. Browser tests cover correct error timing, progress boundaries and no stale successful state. Commit.

### Task 5: GitLab submission reconciliation

**Files:** internal/app/gitlab.go and tests; internal/gitlab client files as discovered; store metadata and browser assets.
**Interfaces:** Versioned SubmissionAttempt records request fingerprint, branch and completed stages, no token/payload. Read-only inspection yields no-write/matching-partial/completed/conflict/unknown. Explicit resume endpoint operates only on proven exact matching content.
- [ ] Test branch creation success followed by commit failure, commit success followed by MR failure, lost response, conflicting existing branch and duplicate MR. Assert exact call counts and no overwrite.
- [ ] Implement checkpointed stages and read-only reconciliation; allow explicit safe resume only after exact content/project binding. Preserve failed attempt evidence.
- [ ] Browser regression covers safe recovery instructions and completed submission links. Commit.

### Task 6: Integrated independent validation

- [ ] Review all Stage 1 changes independently; resolve material findings.
- [ ] Run supported Linux go test ./..., go test -race ./..., go vet ./... and changed browser fixture suites after workers finish.
- [ ] Privacy-scan explicit public diff; confirm no private harness included.
- [ ] Perform browser live read-only reconciliation of existing partial sandbox operation before any new mutation; then independently approved GitLab recovery/rollback with before/after resource evidence.
- [ ] Record exact revision and limitations. Publish preview only if release checks pass; full live acceptance remains pending if native effects/rollback cannot be verified.
