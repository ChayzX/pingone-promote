# ADR 0009: Secrets come only from pingone_secrets

- Status: Accepted
- Date: 2026-11-03
- Issues: #60, #63, #71

## Decision

- Secrets never appear in tfvars. A file holds only `@@secret:<key>@@`
  placeholders, or `secret = "<key>"` on variables with `data_type = "secret"`.
- Keys match `^[a-z0-9_]{1,128}$` and are the same in every environment.
  The charset is deliberately conservative (lowercase letters, digits and
  `_`) so that keys can later map one to one onto the enterprise pipeline's
  secret variable names. The pipeline declares secrets in a
  `customer_secrets.yaml` (variable name to Vault location) whose exact format
  is not yet confirmed. An early draft allowed `.` in keys
  (`pingone_sso.client_secret`); it was switched to `_`
  (`pingone_sso_client_secret`) before any file was published. The module
  validates the key charset of `pingone_secrets` too. There is no Vault or
  secret-ref plumbing in this phase; the pipeline supplies the map.
- The root variable `pingone_secrets` (`map(string)`, sensitive) is supplied
  by the pipeline as `TF_VAR_pingone_secrets` JSON.
- `terraform_data.contract_secrets` fails the plan and lists the missing key
  **names**.
- Substituted values are JSON-escaped with
  `substr(jsonencode(v), 1, length(jsonencode(v)) - 2)`. `trim()` is not used:
  it would eat an escaped trailing quote. Tests cover secrets containing `"`,
  `\` and a trailing quote.
- `check` scans every string, including JSON values, for masked values
  (`******`), private keys and JWTs. It also flags secret-looking names
  (`clientSecret`, `apiKey`, `*token`, `*password`) whose values are literals.

## Alternatives

- Provider-side secret references or write-only attributes: not available in
  provider 1.23.
- A secrets file next to the tfvars: rejected. It would end up in Git.

## Consequences

- Secrets are in Terraform state (ADR 0001).
- `pingone-promote check --required-secrets` lists the key names the pipeline
  must supply.
- The name heuristics can produce false positives. A value that is entirely
  a DaVinci expression (`{{...}}` or `${...}`), or that contains a secret
  placeholder, is not flagged.
