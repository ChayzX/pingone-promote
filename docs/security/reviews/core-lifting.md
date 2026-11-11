# Bounded independent core-lift review

Reviewer: `core_review`, different from original lift author and root fixer. Date: 2026-11-11. Read-only repository review; independent tests use `/home/cpederson/pingone-promote-research/core-review-overlay/lift-overlay.json`, replacing the test source only for `go test`. No source edits/commits/pushes.

Read current `core_lift.go`, all `core_lift_test.go`, the hook in `rewrite.go`, and the classify → lift → rewrite → scan sequence in `neutralize.go`. Compared writable sensitive path names against actual pinned v1.24.0 provider schema; all writable sensitive schema attributes printed by the traversal are represented in coreSecretPaths. The map additionally contains gateway credential handling. User resources remain excluded by the exporter; their defensive sensitive path does not authorize user export.

## Verified invariants

- `liftAttributes` invokes `liftCoreAttributes` after operator-specified rules. Environment binding fields become per-environment inputs; ordinary business values and existing references are preserved. No blanket URL heuristic lifts shared descriptions or protocol behavior.
- Nested objects/lists and repeated blocks use structural schema paths plus concrete indexed value paths, so repeated credentials and environment bindings have distinct variable names.
- Known credential literals and masks create typed KindSecret variables and never enter SourceValues. Literal secret mappings validate names, retain the generated origin key, and rewrite the actual block to the mapped Vault variable.
- Core environment literals enter SourceValues only for their source overlay; their variable kind is per-environment. Endpoint values containing URL passwords/query credentials or suspected secret content fail without echoing the value. Explicit parent lift rules cannot downgrade a nested schema credential: the parent becomes KindSecret and SourceValues is deleted.
- Root's `protectCoreSecretRefs` fixes the previously reproduced existing-variable leak. At a schema-sensitive field it resolves the actual variable, rejects reserved environment/import inputs, removes both origin/mapped SourceValues, promotes KindSecret, honors explicit SecretNames, and checks mapping collisions. Existing sensitive declarations retain their earlier classification. The rewrite hook uses the rename map to update actual traversals; pipeline secret values remain absent from overlays.
- Concrete collection/object secret types survive into the root sensitive declaration through the separately reviewed typed-secret integration. Secrets are excluded from required environment overlay values; non-secret environment variables continue requiring each target value.

## Independent verification

```text
go test ./internal/neutralize -run TestCoreLift -count=1
ok pingone-promote/internal/neutralize 0.055s
```

Read root evidence `evidence/core-lift-final.log`, which also records a passing CoreLift run (0.040s). Independent overlay adds `TestCoreLiftIndependentExistingCredentialMapping` to the existing suite: a schema-sensitive nonsensitive `gateway_bind` variable with source auto.tfvars and mapping LDAP_BIND must become a typed secret, preserve origin gateway_bind, rewrite the block to var.LDAP_BIND, and omit both old/new SourceValues. That expanded suite passed (0.043s).

## Remaining direct-HCL edge found

**P2: Unsupported function expressions can retain schema-sensitive source literals.** `bind_password=join("", ["source-password"])` is valid Terraform HCL. `expr.Value(nil)` cannot evaluate the function; the current recursion handles objects/tuples/parentheses but not function calls, then returns success. The actual shared gateway block still contains the source password. This is outside normal canonical provider-state rendering, which emits sensitive inputs as variable references, but contradicts the explicit direct-HCL protection contract in this file's comment. The narrow safe response is a sanitized fail-closed error for an unsupported credential expression, or a traversal that guarantees all source literal credential material is removed. Do not merely rely on generic secret-pattern scanning for known schema-sensitive paths.

Independent overlay `TestCoreLiftIndependentComputedCredentialCannotRetainSourceLiteral` accepts either sanitized rejection or successful removal; it currently fails:

```text
--- FAIL: TestCoreLiftIndependentComputedCredentialCannotRetainSourceLiteral
    core_lift_test.go:331: schema-sensitive credential literal survived inside function
FAIL pingone-promote/internal/neutralize 0.044s
```

Root was notified. Reproduction:

```sh
source /home/cpederson/pingone-promote-research/env.sh
go test -overlay /home/cpederson/pingone-promote-research/core-review-overlay/lift-overlay.json ./internal/neutralize -run TestCoreLift -count=1
```

No real tenant secret import, target provider plan/apply, or arbitrary-HCL completeness is claimed. Final post-fix status may be appended below.

## Post-fix review and CI artifact setup

Root now fails closed on unsupported sensitive function/template expressions; the original independent computed-password `join` reproduction passes, as does the existing-reference mapping regression (expanded independent suite 0.046s). Sensitive IndexExpr/RelativeTraversal cases were still allowed without verifying a reference-only base: reviewer additionally reproduced `["source-password",var.existing][0]` retaining the source literal. Root was notified to close the same unsupported-expression class completely. Current repository CoreLift run also found a test-fixture-only HCL syntax error in the new environment identity case (multiple arguments in a single-line block); no production failure is inferred from that fixture syntax.

New environment-local paths cover all pinned writable environment attributes: description, license_id, name, region, services, solution, type (with only the environment-specific service tags/console/bookmark URLs selected inside services). This prevents source SANDBOX/name/region/license values from entering shared rollout definitions. Existing reference inputs remain references and graph environment-local metadata keeps them per-environment.

Read the new `.github/workflows/ci.yml` importer-artifact step and `scripts/install-tools.py`, plus the actual `TestProviderReaderRealProviderTLSAPI` skip condition. The installer validates the locked platform and artifact SHA-256, writes a verified provider ZIP, and returns its absolute path in tools.providerZip. The workflow exposes that path as PINGONE_PROMOTE_PROVIDER_ZIP via GITHUB_ENV; the subsequent test step already exports PINGONE_PROMOTE_TERRAFORM. These are the exact two inputs needed to execute the real-provider local TLS fixture instead of skipping. Download/install/JSON lookup failures stop the job; no credential or tenant API is needed by this setup. No CI setup finding identified; this is source review, not a completed GitHub Actions run.

## Final independent approval after fixes

**Approved for the bounded reviewed scope; the reported source-sensitive expression finding is fixed.** Latest actual-source CoreLift suite passes (0.049s), including function/template/index fail-closed regressions and the corrected multiline environment identity/protection fixture. Latest independent overlay tests pass (0.026s): existing credential mappings preserve secret/type/origin and remove both SourceValues keys; direct credential function and indexed-expression reproductions now reject safely without echoing source material. Only pure ScopeTraversalExpr references are retained for derived credential values; computed IndexExpr/RelativeTraversalExpr no longer bypass rejection.

Environment identity/protection rules were re-read against the pinned writable environment schema. New variables remain environment-local; source SANDBOX/name/region/license data is held in source values rather than shared definitions. CI importer-artifact setup remains approved by static review as described above. No unresolved finding remains from this bounded core-lift/CI review.

Also reviewed the bounded provider-reader test changes: cancellation waits for a freshly created child listener before cancelling; parent-exit mode remains a distinct case; both assert the descendant listener stops after reader return and retain cleanup. The real-provider fixture timeout increase from 60s to 180s affects the test's reader instance only, not production defaults or security assertions. These changes improve evidence under CPU contention without relaxing no-management-write or process cleanup checks.

Reviewed snapshot hashes: core_lift.go `8a7ef9308fa83e8392ef04cabfc29e8845a05046f82d0c03179bff9ac5111169`; core_lift_test.go `49190d348434c9f7d691c2a9be483aba40a8ee1f1ca8a6e5e53350dbb661c363`; CI workflow `24b59a8bf2dc8c8bcff238a11b2c3bed216dbf8da56dc62b4f7e43b504d641cd`.

Additional independent bounded lifecycle verification: `go test ./internal/exporter -run TestProviderReaderKillsDescendantsOnCancellationAndParentExit -count=1` PASS (7.901s), covering both child-ready cancellation and parent-exit cleanup cases.
