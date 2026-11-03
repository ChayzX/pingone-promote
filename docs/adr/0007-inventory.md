# ADR 0007: Inventory from the PingOne Platform DaVinci admin APIs

> **Superseded by [ADR 0014](0014-source-only-rollout.md) and [ADR 0015](0015-pingcli-terraformer-exporter.md)** (2026-11-03). Kept for history; the v1 code it describes is deleted in migration step 10.

- Status: Superseded by 0014/0015
- Date: 2026-11-03
- Issues: #60, #61, #62, #65

## Decision

The app reads source environments through the PingOne Platform DaVinci admin
APIs, using a read-only worker with the DaVinci Admin Read Only role. It
issues only GET requests through the hardened `pingone.Client`: origin and
path pinning, no redirects, size limits and sanitized errors. The client now
returns a typed `HTTPError` so failures can be classified. The early-access
snapshot API is dropped.

### Endpoints (verified)

The paths, `_embedded` keys and JSON field names below were checked against
`pingone-go-client` v0.13.0, the client `terraform-provider-pingone` v1.23.0
uses:

| Read | Path under `https://api.pingone.<tld>/v1` | Collection key |
| --- | --- | --- |
| Flows (list) | `/environments/{env}/flows` | `flows` |
| Flow (full graph) | `/environments/{env}/flows/{flowID}` | n/a |
| Connector instances | `/environments/{env}/connectorInstances` | `connectorInstances` |
| Variables | `/environments/{env}/variables` (paged with `_links.next`) | `variables` |
| Applications | `/environments/{env}/davinciApplications` | `davinciApplications` |
| Flow policies | `/environments/{env}/davinciApplications/{appID}/flowPolicies` | `flowPolicies` |

### Assumptions (Phase 0 spike, #61 and #62)

- The flow list may omit `graphData`, so every flow is fetched again by ID.
- Only the variables collection declares a `next` link in the client models.
  The reader follows `_links.next` and checks `count` for every collection.
- Connector `properties` are a flat `{prop: {type, value}}` map, and stored
  secrets come back masked as `"******"`. The translator never emits a value
  it classifies as secret, whether or not it is masked.
- The DaVinci Admin Read Only role permits every GET above.

### Partial inventory

`davinci.Read` never fails as a whole. Each kind that cannot be read, or is
read only in part, is recorded in `Inventory.Partial`, with a reason
(`unauthorized`, `forbidden`, `not_found`, `rate_limited`, `server_error`,
`http_error`, `transport`, `malformed`, `incomplete`, `credential`), the HTTP
status, and a count of failed items. Callers must not treat a missing item as
deleted when its kind is partial. Application client secrets and API key
values are never decoded.

## Alternatives

- Configuration Management snapshots (today's path): rejected. The API is
  early access, it needs Promotion Admin, and it is not a Terraform-shaped
  inventory.
- `terraform import` or `plan -generate-config-out` as the inventory:
  rejected. They need write credentials and state, and their output is not
  canonical.
- Depending on `pingone-go-client` directly: deferred. Its generated models
  would bring a large dependency tree and a second HTTP stack. The hardened
  client already covers these six GETs.

## Consequences

- Live responses may differ from the client models. The Phase 0 spike
  records real responses as fixtures.
