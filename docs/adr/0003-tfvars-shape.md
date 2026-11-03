# ADR 0003: tfvars shape, references and logical keys

- Status: Accepted
- Date: 2026-11-03
- Issues: #58, #60, #64, #66

## Decision

Each environment has one file, `environments/<env>.tfvars`, which follows
tfvars schema v1 (`internal/tfvars/schema.go` is authoritative):

- Collections are maps keyed by **stable logical keys**. The key is the same in
  every environment, so promotion copies definitions byte for byte.
- Cross-references inside JSON are placeholders: `@@connection:<key>@@`,
  `@@flow:<key>@@`, `@@variable:<key>@@`, `@@secret:<key>@@`,
  `@@param:<key>@@` and `@@env:id@@`. Later phases add `@@p1:<type>:<key>@@`.
  `@@@@` is a literal `@@`.
- Structured attributes use plain key fields: node `connection = "<key>"`,
  variable `flow = "<key>"`, policy `distributions[].flow = "<key>"`.
- Environment-local, non-secret values go in `parameters`. Promotion never
  copies them.
- Placeholder kinds are limited by location. Connector properties accept env,
  param and secret. Variable `json_object` and flow output schema accept env
  and param. Flow node properties accept every kind. Placeholders in plain
  (non-JSON) strings are rejected because nothing resolves them.

### Logical keys (#66)

- Keys match `^[A-Za-z0-9_-]{1,128}$`. Secret and parameter keys match
  `^[a-z0-9_]{1,128}$` (no dots, so that they can map onto pipeline variable
  names; see ADR 0009).
- Key encoding is lossless and depends only on the resource's identity, never
  on order or PingOne IDs (`internal/keys`). `A-Z a-z 0-9 _` are kept,
  `-` becomes `--`, and every other byte becomes `-XX` (uppercase hex).
  Decoding accepts only the canonical form. Names longer than 128 encoded
  characters are rejected rather than truncated. Two resources with the same
  name are reported as a collision for the user to resolve.
- **Variable keys always encode the context**: `<name>_<context>`, or
  `<name>_flow_<flowKey>` for flow variables. This covers every context the
  provider allows (`company`, `flow`, `flowInstance`, `user`). In the name
  part, `_` is also escaped (`-5F`), so the first `_` is always the separator.
  A company variable and a flow variable with the same name, or one flow
  variable name on two flows, therefore get distinct, stable keys. This
  deliberately avoids the pingcli-plugin-terraformer bug where such variables
  collide.
- Validation rejects a variable whose key is not the derived key, and any two
  `davinci_variables` entries with the same (name, context, flow) identity.
  The module repeats the identity check in `terraform_data.contract_variables`.
- Map keys are always written quoted, which `terraform fmt` preserves.

## Alternatives

- Keying by PingOne ID: rejected, because IDs differ per environment.
- Keying by display name: rejected, because names are not unique and contain
  arbitrary characters.
- Order-based suffixes for duplicates (`name_2`): rejected, because the key
  would depend on listing order.
- Variable keys without the context: rejected, because of the
  terraformer collision bug.

## Consequences

- Renaming a resource changes its key. This is planned as destroy and create
  unless the app emits a `moved` block (a later PR).
- Keys are less pretty for names with spaces (`PingOne-20SSO`). In exchange
  they are reversible.
