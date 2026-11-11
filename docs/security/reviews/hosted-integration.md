# Independent review of root integration changes

Reviewer: `core_review`, a different author from root. Date: 2026-11-11. Read-only source review; no repository edits/commits/pushes, tenant calls, or target plan/apply by reviewer.

Reviewed root changes in neutralize input/neutralize/render/rewrite, promote adoption/merge/index and tests, request engine/request, server config/server/handlers/templates and related tests. Reviewed actual control flow through server preview → request run → neutralization → merge → blockers → captured publication.

## Conclusions from source

No adoption, implicit hosted update, or typed-secret bypass found in the requested integration paths:

- **Built-ins require adoption in every environment.** Core `markCoreAdoption` records built-in/default/environment-only objects. Export adoption metadata survives composite merges and is forwarded by request engine into neutralization. `promote.validateAdoption` walks source + deduplicated rollout environments and every written adopt-only address, requiring a nonempty import_ids entry before readiness. Source import suffixes populate only the source overlay; targets need explicit `Request.ImportIDs` except environment-only singletons. Missing IDs become `missing_import` blockers; request run checks blockers before committing.
- **Singleton imports use each environment's own ID.** `resourcecatalog.EnvironmentOnly` derives the environment-only import form from the pinned catalog. Neutralizer records source singleton import as the `environment` marker. `environmentImportIDs` adds that marker for each target overlay. `neutralize.ImportBlock` uses `id = var.pingone_environment_id` for singleton types, not `env/environment`; marker values control presence only. Explicit singleton adoption values other than `environment` are rejected.
- **Hosted Git definitions stay unless update is explicit.** Server preview passes `NoRequestFile:true`; request run translates that to `ExplicitUpdatesOnly:true`. Merge's changed selected blocks then update only under `ChoiceUpdate`; changed dependencies already use explicit update. Kept definitions do not become written blocks, avoiding new variable/import side effects from a discarded version. New blocks remain addable. Legacy CLI callers retain their existing selected-update contract because the flag is hosted-only.
- **Secret root types are preserved.** `SecretVariableBlock` receives the provider-derived neutralized type; map/list/object secrets remain typed at both root and child declarations. Sensitive and nullable=false behavior remains. Request and merge both reject secret variables supplied through ordinary environment values. Sensitive declaration classification precedes graph environment-local metadata, so metadata cannot downgrade a secret to a public overlay input.
- **Adoption values stay bounded and constrained.** Request validation checks environment names, full module resource address syntax, length, suffix shape, and secret patterns. Merge additionally restricts environments to source/rollout, module prefix to the chosen layout, addresses to actual existing/exported managed definitions, and types to importable allowlisted resources. Server accepts adoption edits only for previously offered blocker fields or already retained request values; unknown submitted adoption fields are ignored. HTML templates autoescape values. Request validation is called again before saving the updated selection.
- **Default server wiring includes core.** Config requires a pinned v1.24.0 provider ZIP and digest. The default exporter factory constructs Composite(CoreExporter(APIInventory+ProviderStateReader), DaVinci Runner). Optional service selection is copied from validated operator config; otherwise BOM gates families. User request data does not control API origin overrides.

## Root changes re-reviewed after independent findings

The new explicit `Export.EnvLocalVariables` metadata is a sound contract for graph-generated reference inputs: clone deep-copies it; merge unions it; rewrite marks generated inputs; request forwards it to `neutralize.Input`; neutralizer keeps those inputs environment-local after checking sensitive classification. The strengthened independent test checks the actual neutralized connector block contains no unknown source UUID and variables are per-environment. This catches the earlier sourceValues auto.tfvars fix that otherwise silently re-inlined a source ID.

Scoped-role label disambiguation initially introduced an ORGANIZATION scope failure because the real pinned SDK scope model has only id/type and no name; organization scopes are absent from the environment catalog's names map. Reviewer notified root to use stable singleton semantic identity (like environment scope) rather than requiring nonexistent API scope.name. Final stable confirmation is recorded below.

## Limits and observations

- Source provider pinning does not by itself pin target provider resolution: generated child/root versions still originally declared `>=1.19.1, <2.0.0`, and existing Git version files are retained. Root was notified to tighten this if an exact target v1.24.0 guarantee is required. No target provider compatibility or real plan acceptance is implied by this review.
- Existing Terraform state is not queried by this merge path; a definition already in Git is treated as managed. Adoption checks apply to blocks being written, consistent with the requested keep-existing-Git behavior.
- The selected-resource UI now also offers an exclude choice, while request selection correctly rejects excluding the still-selected item. This is a minor UX inconsistency, not an authorization bypass; operators can use the selection checkboxes.
- Review validates the new fields against the surrounding existing validation/blocker/publication flow; it is not a fresh audit of all existing server auth, GitLab publication, or CSRF code.

## Verification

Independent source review completed. `go test ./internal/neutralize ./internal/promote ./internal/request ./internal/server -count=1` was launched locally with the authorized tool environment; final result pending at time of initial report. Focused exporter + overlay run caught a fixture-only regression in `inventoryHAL()` empty nil slices becoming JSON null under stricter collection validation; root was notified. Final results will be appended once the latest fixes are stable.

## Latest independent evidence

The final focused exporter + strengthened overlay suite passes (1.914s); explicit graph environment-local metadata now survives real neutralization without re-inlining unknown source IDs. Organization-scoped role labels now use the stable `organization` singleton identity, matching the actual SDK model's id/type-only scope shape.

A four-package run overlapped root's metadata edits and therefore reported transient missing-field compile failures. It also exposed a real separate lift regression. A later targeted integration run, after metadata existed, independently reproduced:

```text
--- FAIL: TestCoreLiftExistingCredentialVariableDropsSourceValue
    core_lift_test.go:293: credential variable was not made sensitive
FAIL pingone-promote/internal/neutralize 0.116s
```

This test has `bind_password=var.gateway_bind`, a nonsensitive declaration and source auto.tfvars value. The credential must be upgraded to a secret and removed from SourceValues. Root was notified; fixing author owns this lift path. Adoption/merge/server focused test result from the same targeted run is pending. This report does not claim final all-package readiness while that regression remains.

The targeted run completed: `internal/promote` PASS 9.863s; `internal/request` PASS 7.748s; `internal/server` PASS 0.064s. It exercised built-in adoption, singleton imports, typed secrets, hosted explicit update, hosted no-request-file behavior, default composite wiring, repeated adoption edits, and provider/service config requirements. The only package failure was the credential-lifting regression above. No repository edits were performed by this reviewer.

Follow-up bounded credential review: root fixed the earlier existing-variable regression; independent `go test ./internal/neutralize -run TestCoreLift -count=1` now PASS 0.055s, and expanded mapping/source-value regression PASS 0.043s. See `core-lift-review.md` for that fix's source review and one remaining unsupported direct-HCL credential-expression edge discovered afterward.

Final bounded follow-up: the unsupported credential expression issue is now fixed and independently verified (actual CoreLift suite PASS 0.049s; independent mapping/function/index overlay PASS 0.026s). The corrected environment protection fixture passes. See the final approval in core-lift-review.md. No unresolved finding remains from the requested root integration/core-lift/CI review scope.
