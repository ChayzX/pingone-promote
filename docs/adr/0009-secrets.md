# ADR 0009: Secrets come from pipeline-injected variables, never from tfvars

- Status: Accepted (revised 2026-11-03 for the confirmed pipeline format)
- Date: 2026-11-03
- Issues: #60, #63, #67, #71

## Decision

- Secrets never appear in tfvars. A file holds only `@@secret:<key>@@`
  placeholders, or `secret = "<key>"` on variables with `data_type = "secret"`.
- **Key rule:** `^[A-Za-z][A-Za-z0-9_]{0,127}$`, case-preserving. The key is
  the same in every environment. Each key is also a Terraform root variable
  name, so it can equal an existing enterprise secret name such as
  `client_secret`, `basicAuthCredDV` or `davinci_variable_PingFedAuthNCreds_value`.
  (Earlier drafts used `^[a-z0-9_.]+$` and then `^[a-z0-9_]{1,128}$`. Both
  were replaced before any file was published, because existing pipeline
  names are mixed case.)
- **Pipeline injection.** The enterprise pipeline declares secrets in
  `customer_secrets.yml`. This is GitLab CI's `secrets:` keyword in a hidden
  `.customer-secrets` job, using the short Vault syntax
  `<path>/<field>@<mount>`. Each entry becomes its own environment variable,
  `TF_VAR_<name>`. One file serves every environment, because
  `${VAULT_PATH_ENV}` selects the environment. Every PingOne secret lives at
  `apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/<field>@secret`; only
  `<field>` varies, and it may differ from the variable name.
- Terraform ignores `TF_VAR_*` for undeclared variables, so the root stack
  declares **one variable per secret** in a generated `secrets.tf`:
  `variable "<key>" { type = string, sensitive = true, default = null }`, plus
  `locals { pingone_secrets = { "<key>" = var.<key>, ... } }`. This feeds the
  module's existing `pingone_secrets` input (`tfvars.WriteSecretsTF`). The
  earlier single `TF_VAR_pingone_secrets` map variable is removed.
- An unset secret stays `null`. The module treats null as missing, and
  `terraform_data.contract_secrets` fails the plan listing the missing key
  **names**.
- **Naming new secrets.** The generated names are only suggestions, and each
  rule is one function in `internal/translate`:
  - DaVinci secret variables: `davinci_variable_<VariableName>_value`;
  - connector properties: `PingOne_DaVinci_connection_<ConnectionName>_<property>`.
    This is a best guess from a truncated example line;
  - flow node properties: `PingOne_DaVinci_flow_<FlowName>_<node>_<property>`
    (an assumption).

  Characters outside `[A-Za-z0-9_]` become `_`. A collision fails and asks the
  operator to choose a name; names are never auto-suffixed. The main operation
  is *mapping* a tfvars secret key to an existing declared entry, so the
  required-secrets output lists existing declared names as candidates.
- **`customer_secrets.yml` editing** (`internal/pipelinesecrets`):
  - The editor inserts lines; it does not re-encode the YAML. Round-tripping
    with no change is byte-identical.
  - New entries go at the end of `.customer-secrets.secrets`, using the
    file's indent and blank-line style, and need only a variable name and a
    field (the field defaults to the name).
  - The path prefix `apps/pingone-davinci/${VAULT_PATH_ENV}/pingone` and the
    mount `secret` are one overridable configuration value.
  - Existing entries are never removed or rewritten. Commented-out entries
    count as **not declared** and are reported as "commented out" when
    referenced; they are never edited automatically.
  - `check --customer-secrets <file>` fails when a referenced secret is not
    declared. It warns about unused entries and about entries whose path does
    not match the prefix and mount pattern (hand-made exceptions still work).
- Substituted values are JSON-escaped with
  `substr(jsonencode(v), 1, length(jsonencode(v)) - 2)`. `trim()` would eat an
  escaped trailing quote. Tests cover `"`, `\` and a trailing quote.
- `check` scans every string for masked values (`******`), private keys and
  JWTs, and flags secret-looking names whose values are literals.

## Alternatives

- A single `TF_VAR_pingone_secrets` JSON map: rejected. The pipeline injects
  one variable per secret, and its file format is fixed.
- Provider write-only attributes or secret references: not available in
  provider 1.23.
- Re-encoding `customer_secrets.yml` with a YAML library: rejected. yaml.v3
  re-encoding reflows indentation and blank lines, so existing entries would
  show up in diffs.
- Auto-suffixing colliding names: rejected. Names must be chosen
  deliberately, because they are also Vault fields.

## Consequences

- Secrets are in Terraform state (ADR 0001).
- A submission commits the tfvars, `secrets.tf` and `customer_secrets.yml`
  together (wiring in #9).
- The variable `client_secret`, used for provider authentication, is outside
  this mechanism; the provider reads `PINGONE_CLIENT_SECRET` (ADR 0001).
- The name heuristics can produce false positives. A value that is entirely a
  DaVinci expression, or that contains a secret placeholder, is not flagged.
