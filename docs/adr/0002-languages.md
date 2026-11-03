# ADR 0002: Go for the app, generator and checker; Terraform for the modules

- Status: Accepted
- Date: 2026-11-03
- Issues: #58, #60

## Decision

- **Go** is used for the local app, the tfvars generator and
  `pingone-promote check`. `github.com/hashicorp/hcl/v2` is the reference
  HCL implementation, and it is the same code Terraform uses to read tfvars.
- **Terraform/HCL** is used for the modules under `terraform/modules/`.
- The UI (a later PR) will be server-rendered Go `html/template` with
  vendored htmx under a strict CSP.
- These parts are kept: the hardened loopback server, keychain store, GitLab
  client and PingOne OAuth client (about 3.1k non-test LOC).

## Alternatives

- TypeScript or Python generators: rejected. They have no first-party HCL
  writer that matches `terraform fmt` byte for byte, and they would add a
  second toolchain.
- CDK for Terraform: rejected. The enterprise pipeline consumes plain HCL
  modules and tfvars.

## Consequences

- One language and one binary, which also runs in CI for `check`.
- New dependencies: `hashicorp/hcl/v2` and its transitive modules (go-cty,
  x/text and others), pinned in `go.mod`.
