# GitLab production implementation plan

**Goal:** Production requests originate locally, but all target-changing production actions execute through an approved GitLab deployment job.
**Spec:** docs/superpowers/specs/2026-10-27-gitlab-production-design.md
**Execution:** Independent bounded work delegated to lower-cost agents per user instruction; parent owns integration, CI authorization and final verification. User delegated design/work; no additional preference approval gate.

1. GitLab REST submission client and local config store: test private project, protected branch, branch→commit→MR, payload escaping, token/redirect behavior; implement only gitlab package.
2. Versioned request/artifact/native runner engine: test pinned snapshots, explicit excludes, config fingerprint, redacted artifacts, exact digest and CI origin binding, separate rollback plan; implement gitops package and pinned inventory helper.
3. Local app and UI: default local execution denied; startup allowlist for explicit non-production; GitLab settings, export/submission API; production controls point to MR workflow; test malicious direct endpoints/alias profiles/stale UI.
4. Live GitLab approval verifier and CLI: verify current job, deployment, protected environment and approvals via official APIs; require trusted target/env and bind artifact to project/pipeline/commit; fail closed on unknown contracts. Implement headless commands with durable no-replay state/status reconciliation.
5. Pipeline template/docs and full integration: offline MR validation, protected-branch plan, approved deploy and rollback, artifacts/resource groups, runner configuration; browser smoke/regressions, full suite/race/vet, cross-builds and independent code review; publish source with actual limitations.

Each implementation writes and runs its failing tests before code. Shared contracts are sent explicitly to each worker. API examples are evidence, not live integration proof. No GitLab account credentials or private project will be requested in chat; configuration is entered locally.
