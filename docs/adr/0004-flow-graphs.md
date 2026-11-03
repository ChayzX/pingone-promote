# ADR 0004: Flow graphs as typed HCL plus canonical JSON heredocs

- Status: Accepted
- Date: 2026-11-03
- Issues: #60, #64

## Decision

- Node, edge, settings, trigger, input and output schema structure is typed
  HCL. Its attribute names match the provider schema
  (`graph_data.elements.nodes[*].data.*`, which is lifted to `graph.nodes`).
- Free-form JSON (`properties`, `json_object`, `output_schema.output.properties`)
  is written as canonical `<<-JSON` heredocs: keys sorted, two-space indent,
  numbers kept as written.
- `${` is escaped as `$${` and `%{` as `%%{`, so Terraform reads the JSON
  literally.
- Strings that are not NFC have their non-ASCII characters `\u`-escaped, so
  HCL's NFC normalization can never change a JSON value.

## Alternatives

- Typing JSON as `any` inside `map(object)`: rejected. Terraform type
  unification fails when elements have different shapes (verified).
- `jsonencode()` in the tfvars: rejected. tfvars must be values only.

## Consequences

- Diffs are line-stable and reviewable, and JSON changes diff per key.
- The node `name`, `id_unique`, `connectors`, `current_version` and
  `published_version` fields are computed and never authored.
