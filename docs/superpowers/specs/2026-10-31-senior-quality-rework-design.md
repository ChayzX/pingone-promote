# Staged quality rework

Status: proposed design, awaiting operator approval.

## Intent and approach

Make the tool dependable enough to withstand a senior engineering review: predictable operation states, recoverable failures, clear authorization boundaries, useful diagnostics, understandable setup, and evidence tied to the version under test. Code size, extra abstractions and test counts are not acceptance criteria.

The operator selected staged refactoring. Each stage must improve a complete operator workflow, pass its own failure cases, and receive independent review before the next stage starts.

Alternatives considered:

- **Staged refactoring, selected:** retain the existing Go application and wire contracts while replacing inconsistent responsibilities behind tested boundaries. This limits migration risk and keeps existing behavior available for comparison.
- **Replace the workflow core first:** permits a cleaner initial architecture but requires a larger migration before its behavior can be proven. It is not the selected approach.

## Constraints

- Production planning, execution and rollback remain controlled by protected GitLab jobs. The local application cannot provide a production bypass.
- Retain provenance, approval, digest, resource selection, destination identity, drift and execution replay protections throughout the work.
- Support macOS and Linux. Windows may host verification tooling; it is not a newly supported product platform.
- Keep loopback binding and existing browser security checks. Hosting a multi-user RHEL service requires a separate authentication and deployment design.
- Keep tenant configuration, identifiers, credentials and raw service evidence outside the public repository. Test fixtures use synthetic data.
- DaVinci remains a separate setup and build. Do not merge main into its branch. Review and port only individually applicable fixes to its flow JSON and embedded logic, with its own verification.
- Preserve existing profiles, history and reviewed artifacts through an explicit compatibility policy. Do not silently reinterpret an old approved plan under new semantics.

## Evidence motivating the first stage

The existing execution intent and GitLab gates provide substantial protection and should be retained. However:

1. `internal/gitops/engine.go` can create a native forward or rollback draft and subsequently fail while waiting or reviewing. The caller may receive a partial artifact, but the runner does not reliably checkpoint the new identity before later validation or output failure. The actual service's duplicate-draft behavior is not established; loss of a recovery handle is the code-level concern.
2. `internal/app/gitlab.go` intentionally blocks repeated submissions after a write may have happened. A branch, commit or merge request can nevertheless be left without an operator-facing reconciliation workflow.
3. Native lifecycle classifications exist in the app, GitOps engine and runner. No current status mismatch was established in the bounded audit, but separate authorities make future response changes risky.
4. The API boundary returns flattened errors while the UI, runner and logging apply different presentation rules. The recent `promotionErrors` correction demonstrates why decoding, classification and presentation need distinct responsibilities.

A live sandbox operation partially applied and returned HTTP 500. That response alone does not establish whether the initiating request, service behavior or environment caused the failure. Recovery remains unverified. Refactoring must not erase that limitation or claim to repair the service.

## Stage 1: recoverable operation lifecycle

### One lifecycle authority

Introduce a small, pure lifecycle model for operation phase, observed native state, dispatch certainty and permitted next actions. The domain service owns transitions; transports decode observations; the UI and CLI present them.

Keep these facts separate:

- Preparation and validation phase.
- Whether a mutation was never dispatched, may have been dispatched, or has a recorded native identity.
- Whether native execution is pending, completed, failed or unknown.
- Whether resource effects have actually been verified.

Unknown service states never imply completion or permission to execute. A failed native execution can have partial effects. A marker on disk is evidence of an attempt, not proof of successful dispatch or completed application.

### Checkpoint mutation attempts

Extend metadata-only operation records to cover forward draft creation, rollback draft creation and execution. Store an attempt before a non-repeatable request, then checkpoint a returned native identity before polling, review generation or final artifact output. A checkpoint write failure after an accepted response must preserve the uncertain state and block automatic repetition.

Use the existing private atomic-file facilities behind a narrow attempt-store interface initially. Do not introduce a database or new coordination service without a demonstrated requirement. Local persistence and GitLab artifact retention have different failure boundaries: an ephemeral runner can disappear before artifacts upload. Document that limitation and fail closed when recovery evidence is missing. Do not promise exactly-once execution across lost hosts or independent pipelines.

Add read-only reconciliation for a known attempt. It verifies request/origin/environment linkage, reads native status and reconstructs a review where appropriate. It must not create another draft or execute as a side effect. If no native ID was recorded, require explicit investigation or validated association with an existing operation; never guess among multiple candidates.

Reconciliation cannot waive approvals. A changed or reconstructed review needs its own verified digest and the existing applicable GitLab gate.

### Reconcile GitLab submission

Record the attempted request, branch name and completed submission stages without storing tokens or configuration payloads in operational metadata. After an interrupted submission, inspect the existing branch, commit and merge request through the configured private project.

Report separate outcomes: no write found, matching partial submission, completed matching submission, conflicting state, or unknown outcome. Resume only when the server can prove that the existing content and binding match the exact request. Never overwrite a branch or commit unrelated content. Starting a new request remains an explicit operator action rather than an automatic retry.

### Errors and progress

Define an application failure contract with a stable code, phase, safe message, recovery action and correlation ID. Native HTTP status and allowlisted categories may inform that contract; raw vendor messages and arbitrary codes do not automatically become user-visible text.

HTTP handlers, CLI output and UI rendering consume the same classified failure. Logging remains a separate sanitized projection. Preserve the existing `error` text field during an additive HTTP transition, and version persisted metadata if its meaning changes. Existing command names and flags continue to work unless a documented migration is approved.

Show progress at meaningful boundaries, including inventory capture, review, approval, dispatch and reconciliation. Report pre-dispatch rejection separately from uncertain execution. Credentials and raw native payloads never enter progress events.

## Subsequent stages

1. **API contracts and orchestration:** establish a sanitized compatibility corpus for snapshots, promotion details, steps, mappings, errors and rollback linkage. Separate validation, polling and effectful calls. Centralize each invariant without replacing distinct local and GitLab authorization policies with a permissive shared policy.
2. **Setup and operator experience:** make effective readiness explicit for environment credentials, inventory capability and private GitLab configuration. Separate connection verification from installing or configuring the private pipeline. Verify errors appear at the action that can establish the failure. Test actual macOS Keychain and Linux Secret Service behavior.
3. **Maintainability and release discipline:** simplify module boundaries where demonstrated duplication remains; keep documentation and supported-platform claims current; make source packaging reproducibility assumptions explicit; retain checksums, exact source pins and package lifecycle checks.
4. **Independent acceptance:** verify complete promotion and recovery workflows, selected and excluded resource effects, fault paths and separate rollback against disposable environments. Review the final code and evidence independently. Fixture success never substitutes for this stage.

These are sequenced work areas, not permission to accumulate unrelated changes in one diff. Stage 1 receives a detailed implementation plan first; later stages are refined using its findings.

## Verification gates

- Characterize current safe behavior before structural changes. For each defect, capture a meaningful failing regression before implementing its correction.
- Test failure injection at mutation dispatch, accepted response, identity checkpoint, polling, review, output persistence and process restart. Assert call counts and durable state, not only error strings.
- Verify unknown statuses, malformed error shapes, stale reviews, conflicting submissions and missing recovery evidence fail closed.
- Run appropriate Go tests, race detector and vet on a supported OS, plus browser tests for changed user-visible behavior. Avoid expanding test scope without a change or unresolved concern to justify it.
- Keep traces and fixtures sanitized. Verify public changes contain no live identifiers or credentials.
- Independently review each stage's implementation and evidence before marking it verified.
- Publish a new preview only from the exact verified source revision. Preserve old assets and provide an explicit migration note where needed.

## Completion bar

The rework is complete when all agreed stages pass their gates, operator recovery paths have live evidence, documentation describes the delivered behavior, and an independent review has no unresolved material findings. Production readiness additionally requires verified vendor support and the intended organization's actual runner and approval controls. No aesthetic judgment about authorship can replace those criteria.
