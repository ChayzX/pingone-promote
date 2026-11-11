# Independent core/composite and inventory review

Reviewer: `core_review` (different agent from `composite_engineer`, inventory author, and root fixer). Date: 2026-11-11. Read-only repository review; no repository source edits, commits, pushes, tenant calls, Terraform apply/plan, or main/release branch changes by this reviewer. Isolated Go overlay reproduction source lives in `core-review-overlay/review_test.go`; the overlay adds a virtual package test without changing repository files.

## Scope and method

Read actual `internal/exporter/{core,composite,graph,inventory}.go`, recipes, current tests, core schema/state fixtures, DaVinci export fixtures, provider reader import/render interface, request selection/neutralization integration, and adoption merge handling. Compared every executable recipe import component layout with the pinned provider v1.24.0 resource documentation. Examined pinned provider implementation/schema for populations, default identity provider, MFA application maps, scoped administrative roles, application metadata, and secrets; SDK models for BOM and notification templates. Implementation handoffs were context, not correctness evidence.

Reproduce all independent regressions, from repository root:

```sh
source /home/cpederson/pingone-promote-research/env.sh
go test -overlay /home/cpederson/pingone-promote-research/core-review-overlay/overlay.json ./internal/exporter -run TestIndependent -count=1
```

## Findings delivered to root

### P1: DaVinci variable-valued core IDs do not enter dependency closure

Original `graph.go:rewriteGraph` only inspected module resource expressions and `expression` used `expr.Value(nil)`; no auto.tfvars evaluation context was loaded. A normal DaVinci connector has `properties = jsonencode({clientId={value=var.davinci_connection_*_clientId}})`, with the actual application UUID in the root exported values. The existing fixture `fixtures/dev/ping-export-module/pingone_davinci_connector_instance.tf` demonstrates this format. Consequently a DaVinci-only seed could omit its actual core application and the neutralizer could inline the source ID rather than link a managed core reference. The synthetic composite cycle test's literal `group_id` did not exercise this case. Shared application/secret IDs also require a `client`→`application` hint.

`TestIndependentTerraformerVariableCoreReference` originally failed: `DaVinci variable-valued application ID does not enter core closure: map[]`. Root reports a fix using a reference-only variable context excluding sensitive declarations; independent final rerun pending.

### P1: Excluded core references retained source literal IDs

Original `graph.go:value` returned the literal when a known dependency was excluded or unselected. Neutralization repairs variable/resource traversals and environment literals, not arbitrary catalog ID literals. Thus no-upstream or explicit-exclude core exports could leave a source population/application UUID in shared configuration. The corrected contract is an excluded-resource variable compatible with neutralization's existing managed/unmanaged exclusion handling. The same issue affected known map keys.

`TestIndependentExcludedCoreReferenceNeutralization` exercises selection through `CoreExporter.SelectedExport` and then actual `neutralize.Neutralize`; the corrected path passes. The first overlay attempt lacked the fake reader's variables.tf and was corrected outside the repository; this reviewer did not capture a valid pre-fix end-to-end RED, but original source path was directly inspected and root was notified before its fix.

### P1: Unknown application map keys remained source IDs

`graph.go:value` and mixed-object expression handling only considered keys present in `idx.ids`. Real pinned MFA schema `mobile.applications` uses application IDs as map keys. With an unknown application UUID (for example app family not inventoried), the source key remained literal rather than becoming an environment-local input.

`TestIndependentUnknownApplicationMapKey` originally failed: `unknown application ID map key remains source literal`. Root fix pending at notification.

### P2: Wrapper ID disambiguation missed real field aliases

Inventory deliberately exposes a default theme through `pingone_branding_theme` and `pingone_branding_theme_default`; both share its ID. The real population field is `theme={id=...}`, producing hint `theme_id`. Original resolver lacked `theme`→`branding_theme` and returned ambiguity. Similarly actual role-assignment fields `scope_application_id` and `scope_population_id` must resolve application/population rather than their secret/default wrapper. These field names are in pinned provider schemas and role-assignment examples.

`TestIndependentPopulationDefaultThemeAlias` captured RED (`ambiguous source ID ... theme_id`) and passed after root's alias fix. `TestIndependentScopedApplicationReference` captured RED (`ambiguous ... scope_application_id`); root fix pending at notification.

### P2: Generated unknown-ID connector inputs failed neutralization

Graph-generated unknown reference variables had declarations but no exported value. `neutralize.classifyVariables` ordinarily inlined variables used only by connector resources and rejected a missing exported value, even though graph declared these inputs environment-local. This made the required-input behavior fail specifically inside DaVinci connectors.

`TestIndependentUnknownConnectorReferenceIsEnvironmentLocal` captured RED: `neutralize: no exported value for pingone_davinci_connector_instance_connector_applicationid_374eb18c; export with --include-values or keep it environment-local`. Root reports a fix; independent final rerun pending.

### P2: Distinct scoped role assignments collide in inventory labels

`inventory.go:inventoryObjectName` resolves role-assignment identity to the role name, and List's label includes principal parent and that name but omits scope. Two assignments of the same role to the same group/application/gateway with distinct population/application scopes therefore have the same Terraform address. Inventory fails closed instead of exporting the valid environment. Include semantic scope identity (type and resolved scope name), without adding source UUIDs.

`TestIndependentScopedRoleAssignmentLabels` supplies the same role with named `Population one` and `Population two` scopes and captured RED: `configuration inventory has duplicate stable labels for pingone_group_role_assignment`. Root notified; fix pending.

### P2: Malformed BOM product and collection shape can become false absence

Original BOM accepted a product object with missing/non-string/empty required `type`, silently leaving optional services (including DaVinci) disabled. This contradicts fail-closed malformed BOM handling. `TestIndependentMalformedBOMProductFailsClosed` captured RED for `{products:[{}]}`. Root reports required-type validation fix; final independent rerun pending.

Separately, `inventory.go:records` ignored every `_embedded` value that was not an array. `_embedded:{groups:"not-an-array"}` was accepted as an empty collection. This can report completeness while silently losing a configuration family. Require a valid expected collection array / fail on malformed shape rather than treating type errors as absence.

`TestIndependentMalformedCollectionFailsClosed` captured RED: `malformed groups collection accepted as empty`. Root notified; fix pending.

### Previously existing graph regressions independently verified

The first focused existing suite run failed `TestGraphMixedMapKeysUseApplicationReferences` and `TestGraphMalformedJSONStringPreservesTrailingContent`. Source inspection confirmed mixed dynamic objects skipped ID keys; JSON Decoder accepted a complete prefix and discarded trailing business text. Root corrected key traversal and now guards JSON decoding with `json.Valid`. These fixes need the final stable rerun.

## Safety and correctness observations

- The embedded recipes account for the allowed non-DaVinci resource types and use fixed configuration paths. No recipe enumerates users, sessions, audit, activities, events, or group user members; group nesting uses `memberOfGroups`. Source resource allowlist rejects user resources before core provider imports.
- Inventory configuration requests are GET only; the sole POST is regional OAuth client-credentials authentication. Redirects are refused. Pagination keeps exact origin and collection path and restricts query names. Fixed role/image reads never follow payload URLs. Errors omit response bodies and transport credential details; response/request/time limits are explicit.
- All executable recipe import component layouts match the pinned provider documentation, including nested parent IDs, built-in resource scopes, notification template names, and environment-only singletons. This is static provider/SDK evidence, not successful live imports.
- Source IDs do not form inventory address labels; names, parent names, stable singleton type names, and content digests do. Duplicate stable labels fail closed. The scoped-role omission above is a concrete missing semantic context, not a suggestion to introduce ID-based labels.
- Core dependencies are imported at most once per request, closure has a catalog bound and visited set, and excluded resources are removed before core state reads. Configured enabled DaVinci errors propagate; an explicit disabled service avoids DaVinci calls. Existing fake cycle/long-closure fixtures exercise these mechanics.
- Exporter does not issue delete operations. Merge exposes no ordinary deletion path (`Deleted` remains reserved) and adoption metadata marks built-ins/environment singletons. This does not constitute a live target Terraform deletion/plan guarantee.
- Three no-importer resource families and unreferenced images remain documented inventory limitations. Optional singleton 404 and unavailable generated secret imports currently abort rather than silently disappearing; this behavior is explicitly documented, but no live tenant acceptance was performed.
- Unknown reference variable names currently include a digest of the source value. They are deterministic for the same source input, but not stable across different source environments containing different unknown UUIDs; this is an additional design limitation for repeat promotions from different source environments, not yet separately reproduced as a release-blocking finding.

## Verification status

Initial existing focused suite: FAIL on the two graph regressions above. All seven substantive independent bug classes above were found by reading source and/or concrete overlay reproductions; green synthetic fixtures were not treated as sufficient evidence. Root is actively fixing the notified issues. Final stable independent results will be appended below. No live service entitlement, real provider import semantics, tenant secrecy, hosted security scan, or target plan/apply acceptance is claimed by this review.

## Final exporter confirmation

After root fixes, the strengthened independent overlay plus existing focused tests pass:

```text
go test -overlay /home/cpederson/pingone-promote-research/core-review-overlay/overlay.json ./internal/exporter -run 'TestIndependent|TestCore|TestComposite|TestGraph|TestInventory' -count=1
ok pingone-promote/internal/exporter 1.914s
```

Root copied independent reproductions into `core_review_regression_test.go`; overlay now replaces that test file with reviewer's strengthened copy, avoiding duplicate definitions. The unknown-connector test now forwards the real `EnvLocalVariables` metadata into neutralization and asserts the actual neutralized connector contains no source UUID and its retained variables are per-environment. This strengthened test initially caught source-ID re-inlining even after the earlier error-only test passed; explicit metadata fixed that regression.

Reviewed root fixes: variable-valued reference evaluation is limited to ID/environment fields and omits sensitive declaration values; semantic aliases disambiguate wrappers; excluded refs use canonical exclusion variable names; unknown map keys use environment inputs; malformed BOM required type/collection shapes fail; role labels include semantic scope context with fixed environment/organization singleton names. The empty fixture helper now emits [] rather than null, consistent with strict array validation. No unresolved exporter/inventory finding from this report remains at this snapshot. Unreproduced design limitation of source-dependent unknown-ID variable names remains as described above.

Snapshot SHA-256: core.go `1903eb6a4e503207c72c14214ee1bdd340f2bbd103afc8033ad8f1614d5ea5e8`; composite.go `1436963f408b36f4c0ef4b50e7b85d4bc206745bd0c7683dbb2b6e876be4a14a`; graph.go `54c34cca246353ba5c58217bfce46e7727298dd9f0bf856af65012cc3030f591`; inventory.go `2a811f73b06a894c313240b210a92758648e2bfc8eefb81f09f69d552aa58f45`.
