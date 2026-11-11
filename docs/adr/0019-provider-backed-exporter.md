# ADR 0019: Provider-backed configuration export without plan

- Status: Accepted design; provider-backed live acceptance pending
- Date: 2026-11-11
- Extends: ADR 0018
- Retains: ADRs 0014–0016 for neutralization, additive merge and adoption

## Decision

Keep the pinned DaVinci terraformer where it is supported. Add a second Go exporter for provider configuration resources: explicit per-resource REST inventory, then a pinned provider's import/read path in disposable local state, then schema-aware HCL rendering. The server never executes `plan`, `apply`, `plan -generate-config-out`, or PingOne native promotion APIs. Enterprise CI retains deployment ownership.

The inventory is an explicit allow-list derived from all **101** managed resource types in the v1.24.0 provider schema. `internal/resourcecatalog` accounts for all 101; five user/user-assignment types are excluded. Unknown resource types fail closed. This registration count includes both provider frameworks; earlier source-registration-only counts were incomplete. A registered type is not evidence that its inventory endpoint, import ID or read permissions have been verified. No users, user data, sessions or audit logs may be requested, including as incidental reads by a whole-service collector.

## Alternatives considered, in order

1. **Upgrade the existing terraformer.** The inspected published release is [v0.6.0](https://github.com/pingidentity/pingcli-plugin-terraformer/releases/tag/v0.6.0), source commit `9aa51edd1930b1b2724183f7c56911aad0c47556`. Its enabled definitions and static API dispatch cover seven DaVinci handlers. It does not supply platform/SSO/MFA/Protect/Authorize export. Upgrading to that release therefore preserves the existing DaVinci path but cannot satisfy full-provider selection. The inspected source contains embedded definitions and the handler registry under [internal/platform/pingone](https://github.com/pingidentity/pingcli-plugin-terraformer/tree/v0.6.0/internal/platform/pingone).
2. **Use Ping CLI export.** The inspected current v1.11.0 binary has no `platform` command; the upstream removal is tracked in [issue #175](https://github.com/pingidentity/pingcli/issues/175). Historical [v0.8.4 source](https://github.com/pingidentity/pingcli/tree/v0.8.4/internal/connector/pingone) exports adoption/import blocks, not resource argument HCL. The documented follow-up generation uses [Terraform plan with `-generate-config-out`](https://developer.hashicorp.com/terraform/language/import/generating-configuration), forbidden by this server's contract. The old whole-SSO collector also traverses users, violating the inventory boundary even if its output were subsequently filtered.
3. **Add custom YAML definitions.** v0.6.0 embeds its definitions and has no runtime definitions-directory option. New YAML cannot add missing Go API handlers. A fork with handlers, transforms and tests is possible, but would duplicate provider mappings and require ongoing maintenance across every configuration family. Runtime YAML alone is rejected; the binary's static dispatch is the limiting factor.
4. **Add a provider-backed Go exporter.** Chosen. An explicit safe REST inventory supplies display names, canonical resource identities, dependency references and type-specific import IDs. The pinned provider handles resource reads and shape normalization. A local renderer combines schema with show JSON, preserving typed configurable values and replacing secrets before writing any generated HCL. This shares the provider's resource model while keeping the inventory and security policy explicit.

## Import/read and rendering contract

For each permitted source object, the exporter allocates an unindexed root address and writes a minimal resource declaration into a newly created private directory outside the Git checkout. It uses a verified local Terraform binary, an exact provider pin and a local backend. It imports each object with `terraform import ADDRESS ID`, obtains `terraform providers schema -json` and `terraform show -json`, and supplies those bytes plus literal address/import-ID metadata to `RenderProviderState`. [CLI import](https://developer.hashicorp.com/terraform/cli/import) updates Terraform state; it does not generate resource configuration. This use is distinct from import blocks followed by plan-based generation.

The importer receives only source read-only credentials through an explicit child environment allow-list. No rollout credentials, ambient backend configuration or user-selected executable/arguments are accepted. Provider installation must use verified artifacts; no automatically selected provider upgrades. Import invokes provider ImportState/Read implementations, so safe source API behavior must be audited and exercised per type. A read-only worker and API inventory restrictions are required controls; command naming alone is insufficient evidence of remote read-only behavior.

State and show JSON may contain plaintext secrets despite sensitivity markers. Keep them in bounded memory/private temporary files, never in the checkout, logs, preview DTOs, error text or Git. Bound subprocess time/output and inventory pagination; cleanup the owned temporary directory on every exit. Sanitize subprocess diagnostics separately from this renderer. No remote state is read or written by this export path.

The renderer:

- Uses the exact configuration catalog and fails the entire export on unsupported types, malformed import entries, duplicate or unsupported addresses, unknown configured fields/types, or invalid sensitivity/type shapes. It never returns a partial export on error.
- Preserves configurable scalar, object, tuple, list, set and map values, including false, zero, empty collections, map keys, nested block labels and exact JSON numbers. It handles framework `nested_type` attributes as well as legacy nested blocks/types.
- Omits computed-only attributes, including generated current/previous application/resource secrets, DaVinci API keys/OAuth secrets and gateway credentials. Optional+computed is configurable: v1.24.0 webhook `connection_details_headers` is a sensitive `map(string)` and must become a required secret input, not disappear. No attribute-name heuristic classifies secrets.
- Treats provider `sensitive` flags and state `sensitive_values` as authoritative at every nesting level, including legacy typed objects. Configurable secrets become sensitive Terraform variables with schema-derived types, no defaults and no tfvars values. Secret names cannot alias after path sanitization. The downstream neutralizer retains these types when declaring required Vault references.
- Declares the shared `pingone_environment_id` in root and module scopes, uses it for environment references, and retains literal compound import IDs for the existing neutralizer's per-environment adoption conversion. Dependency rewriting and further environment-specific fields belong to the existing neutralizer/graph stages.

Run the existing secret scan on raw exporter artifacts and final public preview/Git payloads. A schema renderer is not proof that arbitrary unmarked application content contains no secrets.

## Pins and verified artifact evidence

These are the Linux amd64 artifacts inspected during this decision, not a promise that other platforms have the same digest. Verify the corresponding platform artifact before execution.

| Tool/artifact | Version/source | SHA-256 |
| --- | --- | --- |
| Terraform ZIP | 1.16.7 | `5454efcf7fc87f0c2113111b9a5c7182f0ca1c6f0f23a1e110a50770a8fe473b` |
| PingOne provider ZIP | 1.24.0 / `d5019dec50baddf344540249219023f839874f2b` | `5265874941d77401600642defe03ae105634a97c4144805441071d08742ed611` |
| DaVinci terraformer executable | 0.6.0 / `9aa51edd1930b1b2724183f7c56911aad0c47556` | `be0bea0f4f7ecb4e33a95f9894793ae9c70bb3480665d9d303c06021b8ae4a98` |
| Ping CLI executable, research only | 1.11.0 / release `077bf45308c69eef6d7098c554928c8dfe0195eb` | `a9e6ff01e59731391826c67c91ea89d6f62fd3fc5ffd6fe28e43f25b6020862b` |
| Historical Ping CLI executable, rejected | 0.8.4 / `8b93d3c06e7c2674d83203f100b09111b600e784` | `d8f83d8b0dcdb953dc8b431653f3c177396af6db1f2c89c49c72dd595a8aa42c` |

Archive checksums above identify downloaded artifacts; executable checksums are independently verified at the server execution boundary. A ZIP digest must not be compared directly with an extracted executable digest. Provider source and release are [v1.24.0](https://github.com/pingidentity/terraform-provider-pingone/releases/tag/v1.24.0).

## Evidence boundaries and consequences

Offline schema inspection confirms 101 registered managed types. Deterministic renderer fixtures cover nested sensitivity, typed secrets, computed-only omission, optional+computed webhook headers, compound IDs, maps and invalid-input failures. Public schema subtrees are used for localized branding, notification email, application mobile integrity and application-secret fixtures. They verify transformation behavior, not tenant permissions or API availability.

No tenant or GitLab calls were made to establish this decision. Complete family coverage requires explicit inventory/import fixtures and a non-production live case for each feasible type. Some secrets are unreadable after creation; missing required values must block readiness and request Vault references. Conflicting/default/injected provider values and current-versus-published DaVinci flow behavior require separate fixtures and live acceptance. The provider can normalize state without exposing every original choice, so imported state is not proof of a perfect source configuration round trip. Keep the coverage matrix honest about unsupported/unverified types and surface failures instead of silently omitting resources.
