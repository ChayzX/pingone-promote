# ADR 0007: Inventory from the PingOne Platform DaVinci admin APIs

- Status: Accepted (implementation in a later PR)
- Date: 2026-11-03
- Issues: #60, #61, #62

## Decision

The app reads source environments through the PingOne Platform DaVinci admin
APIs. It uses a read-only worker with the DaVinci Admin Read Only role. The
early-access snapshot API is dropped. Phase 0 evaluates `pingone-go-client`
against the existing hardened PingOne client.

## Alternatives

- Configuration Management snapshots (today's path): rejected. The API is
  early access, it needs Promotion Admin, and it is not a Terraform-shaped
  inventory.
- `terraform import` / `plan -generate-config-out` as the inventory: rejected.
  It needs write credentials and state, and its output is not canonical.

## Consequences

- API paths beyond `connectorInstances`, masked-secret readback and large-flow
  sizes are Phase 0 questions (#61, #62).
