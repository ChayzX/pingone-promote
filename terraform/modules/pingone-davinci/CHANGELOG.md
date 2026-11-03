# Changelog

All notable changes to the `pingone-davinci` module. Releases are tagged
`pingone-davinci/vX.Y.Z`.

## Unreleased (planned pingone-davinci/v0.1.0)

### Added

- tfvars schema v1 inputs for connector instances, variables (global and
  flow-scoped), flows in tiers t0..t3, flow deploy and enable, applications
  and application flow policies.
- `@@kind:key@@` placeholder resolver with JSON escaping, and the `@@@@`
  escape.
- Contract preconditions for secrets and parameters, references, tier
  ordering, variable rules and identity, and import IDs.
- `for_each` import blocks driven by `import_ids`.
- Per-flow definition digest that drives `flow_deploy` redeploys.
- Requires Terraform >= 1.15.0 and `pingidentity/pingone` ~> 1.23.
