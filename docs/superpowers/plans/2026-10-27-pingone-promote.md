# PingOne Promote implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver a portable local browser application that promotes explicitly selected PingOne configuration resources with mappings, review, deploy and rollback.

**Architecture:** A standard-library Go API client and fail-closed validator feed a localhost HTTP application with embedded static UI. Native snapshots and promotions provide generic resource coverage; profiles/history are local, and secrets are kept separately.

**Tech Stack:** Go 1.24+, standard library, HTML/CSS/browser JavaScript; Python Playwright for development verification only.

**Spec:** docs/superpowers/specs/2026-10-27-pingone-promote-design.md

## Global constraints
- macOS first; preserve Linux/RHEL portability; no shared server deployment in this release.
- Same PingOne tenant; any configuration type exposed by native snapshots can be selected.
- Notification templates and dependencies are optional; never automatically approve them.
- No nonempty, complete, fresh and scoped READY plan means no Deploy.
- Native sensitive variables and rollback limitations remain in force.
- No plaintext credential or raw payload persistence; no live tenant credentials available in this workspace.

## Review focus
- A forbidden template can be hidden on a later page: inspect all pages before execute.
- A matching source ID alone cannot authorize a different target path: validate mapped target boundaries.
- Configuration-record IDs differ from resource IDs: inventory must use resource.id and resourceUrl.
- A remote plan may change after review: compare a fresh digest and serialize local execution.
- Rollback is a new native promotion requiring execution: track both IDs and never claim success on draft creation.

### Task 1: API boundary and inventory
Files: internal/pingone/client.go, inventory.go, models.go; matching *_test.go. Interfaces: Client.Do(ctx, method, path, contentType, body, output), Client.Pages(ctx,path,key), Client.Inventory(ctx,envID), native Promotion/Step/Snapshot models.
- [ ] Write boundary tests using documented fixtures: OAuth authentication environment, pagination including relative links, hostile links, snapshot original IDs and pinned versions.
- [ ] Run tests and record the missing-behavior failure.
- [ ] Implement credential-backed HTTP client with timeouts, safe errors, bounded bodies/pagination, origin restrictions; asynchronous snapshot metadata polling.
- [ ] Run package tests and record results.

### Task 2: Selection, mappings and review validator
Files: internal/promotion/plan.go, plan_test.go. Interfaces: Selection inventory + approved IDs + mapping map; BuildRequest returns native explicit selection/exclusion body; Review validates native steps and returns redacted presentation and Digest.
- [ ] Write tests for no default environment promotion, explicit exclusions, optional notification promotion, reuse mapping, duplicates/unknown resources, second-page unapproved template, mismatched environment/target, unknown operation and changed fingerprints.
- [ ] Run failing tests, implement typed plan creation and validation, then rerun all tests.

### Task 3: Profiles, credentials and history
Files: internal/store/store.go, secrets.go and *_test.go. Interfaces: Store.SaveProfile/ListProfiles/DeleteProfile; Secret(profile), session/env/keychain adapters; durable operation records with IDs/status but no raw configs/tokens.
- [ ] Write tests for atomic durable profile/history writes, 0600 permissions, credential omission, profile identity preservation, malformed data handling and secret-source validation.
- [ ] Run failing tests, implement and rerun suite.

### Task 4: Local HTTP workflow
Files: internal/app/server.go, workflow.go, server_test.go; cmd/pingone-promote/main.go. Interfaces: authenticated JSON endpoints /api/profiles, /api/inventory, /api/plans, /api/plans/{id}, /deploy, /rollback, /api/history; bootstrap cookie + CSRF.
- [ ] Write httptest workflow tests for forbidden-template plan never executing, digest change, replay/double-click prevention, origin/session validation, persisted operation reconciliation, and rollback creation then reviewed execution.
- [ ] Run failing tests, implement synchronous request-sized operations with explicit Refresh/status polling in UI and native asynchronous statuses. Keep non-idempotent writes single-attempt and persist intent first.
- [ ] Run full suite and race detector.

### Task 5: Browser UI and packaging
Files: internal/app/web/index.html, app.js, style.css; README.md; scripts/build.sh; .gitignore; docs/verification.md.
- [ ] Record visual intent/tokens/wireframes and render checklist outside product repository.
- [ ] Implement environment editor, searchable resource inventory, checkbox approvals, automatic/manual target mapping, redacted before/after review with blocked state, deploy confirmation, history and rollback review. Disable stale controls after state changes.
- [ ] Exercise actual browser UI against test-only local Ping fixture: save profiles, load/search inventory, select/map, preview, block hidden template, opt in, deploy, rollback. Capture desktop 1440px, intermediate 900px and mobile 390px; check empty/error states and overflow.
- [ ] Run go test ./..., go test -race ./..., go vet ./..., node --check; cross-build macOS arm64/amd64 and Linux amd64.
- [ ] Document commands, evidence, live integration limitations and precise acceptance steps; build downloadable source and release bundles.

Execution method: inline implementation chosen under user's delegation, with one independent final code review. No further preference gate is needed; user authorized engineering design/work. Self-review: all spec goals map to tasks; credential storage, native variables, rollback limits, and platform limitations are explicit.
