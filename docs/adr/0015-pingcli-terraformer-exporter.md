# ADR 0015: pingcli-terraformer as the exporter, plus a neutralizer

- Status: Accepted
- Date: 2026-11-03
- Issues: #58
- Supersedes: 0003, 0004, 0005, 0007, 0008 (see 0016), 0010, 0011 (with 0014)

## Decision

### Exporter (`internal/exporter`)

[pingcli-plugin-terraformer](https://github.com/pingidentity/pingcli-plugin-terraformer)
(Apache-2.0), pinned at v0.6.0 (commit `9aa51edd1930b1b2724183f7c56911aad0c47556`),
runs as a subprocess in standalone mode. The enterprise config repository
is already its output, so the tool produces the same shape.

Verified against the v0.6.0 source (`main.go`, `cmd/tf.go`, `cmd/export.go`,
`internal/module/generator.go`, `internal/core/orchestrator.go`,
`internal/utils/sanitize.go`, `definitions/pingone/davinci/*.yaml`):

- Standalone mode unless `PLUGIN_PROTOCOL_VERSIONS` is set; we never set
  it. `--version` prints `pingcli-terraformer version <v> (commit: <sha>)`.
- Listing: `pingcli-terraformer export --list-resources` prints
  `<type>.<label>` lines on stdout (no names, no IDs).
- Export: `pingcli-terraformer export --out <dir> --module-dir ping-export-module
  --module-name ping-export --output-format hcl --include-values
  --include-imports --include-resources <addr> ... --include-upstream
  [--exclude-resources <pattern> ...]`. A whole-environment export omits
  `--include-resources` and `--include-upstream`. `--include-resources` is a pflag
  StringSlice: repeatable and split on commas, so patterns with commas are
  refused.
- Credentials only through `PINGCLI_PINGONE_CLIENT_CREDENTIALS_CLIENT_ID`,
  `..._CLIENT_SECRET`, `PINGCLI_PINGONE_ENVIRONMENT_ID`,
  `PINGCLI_PINGONE_EXPORT_ENVIRONMENT_ID`, `PINGCLI_PINGONE_REGION_CODE`.

Safety:

- Config is `{version, sha256, path}`. Before every exec the binary is copied
  into a private 0700 directory, the copy's SHA-256 must match, its
  `--version` must report the pinned version, and only the copy is run (no
  check-then-use race).
- `exec.CommandContext` with explicit argv; no shell; no secret on argv
  (tested by recording the child's argv and environment).
- The child environment is an allowlist: the five variables above, `HOME`
  set to a fresh 0700 directory, and optional `HTTPS_PROXY`, `NO_PROXY`,
  `SSL_CERT_FILE`. Nothing else is inherited (no `TF_VAR_*`, GitLab tokens,
  keychain or CI variables).
- Output goes to a 0700 directory that is size-bounded, scanned with the
  shared secret heuristics (`internal/secretscan`), read into memory and
  deleted.
- terraformer's own "duplicate resource labels" failure is a typed
  `LabelCollisionError`.

### Neutralizer (`internal/neutralize`, hclwrite and the HCL lexer)

Input is the terraformer output; output is the shared definitions plus the
source environment's values:

- **Always lifted:** the source environment ID. `environment_id =
  var.pingone_environment_id` already; every literal occurrence inside a
  string (for example an application redirect URI or a connector `envId`)
  becomes `${var.pingone_environment_id}`, or a bare reference when the
  whole value is the ID.
- **Lifted by rule:** attributes chosen per resource type, by default
  `oauth.redirect_uris` and `oauth.logout_uris` of applications
  (`davinci_application_<label>_oauth_redirect_uris`), and connector
  properties chosen by the operator.
- **DaVinci variable values** are variable-eligible in terraformer
  (`davinci_variable_<name>_value`) and stay environment-local unless the
  operator marks them "the same in every environment" (inlined).
- **Every other connector property** terraformer turned into a variable is
  inlined back as a literal, because it is shared. A literal under a
  secret-looking name is never inlined; it stays a variable and is
  reported.
- **Secrets** (variables terraformer declares `sensitive`) are renamed onto
  `customer_secrets.yml` names through a mapping keyed by terraformer's
  name; terraformer's names are the default (ADR 0009).
- **Dropped:** literal import blocks (their IDs become an address-to-source-ID
  map) and every `*.auto.tfvars` (terraformer writes `secret = ""`, which
  would override the pipeline's `TF_VAR_`).
- **Collisions fail closed.** Two resources with one address, or two values
  mapped onto one variable, are errors. Flow-scoped DaVinci variables that
  share a name on different flows get the flow added to the label
  (`pingcli__<name>_flow_<flow label>`); the merge records a `moved {}` when
  the old address is in Git. terraformer itself refuses such exports today
  (label_fields `[name, context]`), which is reported as an upstream issue.
- **Dependency scan.** `{{global.variables.x}}` references in flows are
  scanned so a read-only worker suffices; variables the export lacks are
  reported.
- Output passes `hclwrite.Format` and `terraform fmt`
  (`tfvars.FormatWithTerraform`), and `terraform validate` in the reference
  root.

## Scoped export and additive merge

The user's requirement: *search for a main flow and export only it; instead
of overwriting everything like the current Ping tool does, update the repo
to add the changes.* terraformer writes whole files (one `.tf` per resource
type), so a naive re-export replaces everything. The merge is therefore
surgical at the resource-block level.

1. **Search.** `--list-resources` returns addresses only; names are decoded
   from terraformer labels (`-0020-` is a space). For ID searches, a flows-only
   export with `--include-imports` supplies IDs and exact names.
   `exporter.Search` matches an exact ID first, then an exact name, then a
   case-insensitive substring. More than one match is ambiguous and is
   listed; the tool never guesses. CLI:
   `pingone-promote export --source <env> --flow "<name or id>" [--repo <dir>] [--dry-run]`.
2. **Scoped export.** `--include-resources <flow> --include-upstream` pulls
   subflows, connector instances and variables. `--include-upstream` never
   follows references downstream, so a second pass adds every closure
   flow's `flow_deploy` and `flow_enable` (same label) and any
   `{{global.variables.x}}` variable. Applications and flow policies are
   included only when selected.
3. **Additive, block-level merge** (`internal/promote`, `internal/hclblocks`).
   Every `definitions/*.tf` is parsed; only blocks whose addresses are in the
   export are replaced (by byte range) or inserted (in label order, as
   terraformer sorts). Every other block, comment and file stays
   byte-identical. A new resource type creates `<type>.tf`, terraformer's
   file name. **A resource missing from a partial export is never a
   deletion**; the request job has no remove operation, so taking a resource
   out of Git is a manual change.
   Root `variables.tf`, `secrets.tf`, `imports.tf`, the module call,
   overlays and `customer_secrets.yml` only receive what the written blocks
   need; existing entries are untouched.
4. **Dependency default.** The selected flow (and its deploy/enable) is
   always updated. A dependency new to the repository is added. A
   dependency that exists and differs from Git is **kept** by default and
   reported as `kept_differs`, because a shared connection or subflow can
   affect other flows; the operator opts into `update` per address. A
   dependency that matches Git is unchanged.
5. **Tests.** The golden test exports only Flow B (changed node, new
   subflow, changed shared connection) from a fixture with three flows and
   asserts: Flow B updated, subflow added, connection kept and reported,
   every byte of flows A and C and their dependencies identical, nothing
   deleted; a re-run is a no-op; `git diff` touches only the expected hunks.

## Whole environment and exclusions

Two later requirements: *export everything*, and *exclude items and
dependencies the same way as including them.*

### Scope

`scope: "all"` (request field, ADR 0017) exports every supported resource:
`export` with no `--include-resources`, so the flow deploy and enable
resources come with the list and no second pass is needed. The merge rules
are the ones above and do not bend:

- additive only; nothing is ever deleted, and a resource missing from Git is
  added;
- there is no single selected item, so every resource that differs from Git
  **keeps its Git version** unless `dependencyChoices` says `update` (role
  `environment`; reported `kept_differs`);
- a re-run against its own result is a no-op (tested on the fixture).

`result.json` stays complete (`changes` lists everything) and adds
`summary.byType`, counts per resource type, so a screen can lead with the
summary. The diff is cut at a line boundary: 1 MiB for a selected request,
512 KiB for a whole environment, whose full diff is the commit or compare
view behind `diffUrl`. The merge request description of a whole-environment
request carries the per-type table and at most 300 rows of changes.

### Exclusions

`exclude: [{type, id?, name?} | {address}]` applies in both scopes and is
resolved like an include (exact ID, exact name, unique substring; ambiguity
is a `preflight` error with candidates). The resolved addresses, plus a
flow's deploy and enable, go to terraformer as `--exclude-resources`. An
excluded resource is never exported or written and its blocks in Git are
untouched; excluding something that is also explicitly included is a
validation error naming both. A dependency choice `exclude` does the same
for one address, and the selected item itself cannot be excluded.

How terraformer v0.6.0 renders a reference to an excluded resource (read in
`internal/core/orchestrator.go`, `internal/filter/filter.go`,
`internal/core/embedded_references.go`):

- `applyUpstreamExpansion` keeps a resource out of the output when it
  matches an explicit exclude, even if a seed refers to it, and marks it
  excluded (`explicitExcludes`); without `--include-upstream` the filter
  does the same (`excludedIDs`).
- `resolveOneReference` then renders an **attribute reference** to it (a
  node's `connection_id`, a flow policy's `davinci_application_id` and
  `flow_distributions[].id`, a deploy's `flow_id`) as a variable named
  `SanitizeVariableName(<type>_<label>_<field>)`, for example
  `pingone_davinci_connector_instance_pingcli__PingOne_id`. It is declared in
  the module's `variables.tf` with the description "ID of <type> resource
  (excluded from export)", passed from the root module call, and valued with
  the original ID in the `.auto.tfvars` under `--include-values`.
- An **embedded reference** (`subFlowId` inside a flow's node `properties`)
  is resolved by `ResolveEmbeddedReferences` against the whole graph and never
  consults the excluded set, so v0.6.0 renders it as a reference to a block
  that is not in the output. Reported upstream as an issue; the neutralizer
  repairs it (below), and the fake terraformer reproduces both behaviours.

The neutralizer takes the excluded addresses (`Options.Excluded`) and
classifies each:

- **Already managed in Git:** references keep pointing at the Git address.
  The terraformer variable is dropped and its uses are rewritten to
  `<type>.<label>.id`; nothing else changes, so it behaves like `keep`.
  Change reason: `excluded; Git-managed copy kept`.
- **Not in Git:** the variable becomes a per-environment **root variable**
  `<type>_<label>_id` (kind `excluded_ref`), declared in the module and the
  root `variables.tf`, passed by the module call, valued in the source
  overlay from the export (terraformer's value, else the catalog's ID for the
  embedded case) and required in every other overlay. A missing value is a
  `missing_value` blocker for that environment with `var` set and a message
  naming the dependency; `envValues` supplies it. Change reason: ``excluded;
  referenced by ID variable `<var>` ``.

Tests: the three-flow fixture in whole scope (adds everything new, deletes
nothing, re-run is a no-op), whole scope excluding a flow (its Git blocks are
byte-identical, argv has the excludes), exclude ambiguity with candidates,
excluding a managed dependency (files identical to `keep`), excluding an
unmanaged one (variable in both `variables.tf`, source overlay value, a
blocker per other environment, `check --root` passes once the values are
supplied) and a `terraform test` with `-var-file` per environment.

## Alternatives

- Our own DaVinci reader and translator (v1, ADR 0007): retired. It
  duplicated terraformer, and the enterprise repository is terraformer
  output already.
- Running terraformer as a pingcli plugin: needs pingcli and its config;
  standalone mode is the same code with a smaller surface.
- Re-exporting whole files: rejected; it overwrites hand-maintained and
  unrelated content.

## Consequences

- terraformer's label and variable naming are part of our contract; a
  terraformer upgrade is a deliberate pin change with golden tests.
- A dependency kept at its Git version may not match what the selected
  flow expects; the report says so and the operator decides.
