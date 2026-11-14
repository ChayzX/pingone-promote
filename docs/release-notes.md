# PingOne Promote preview

This preview is the v2 tool: a DaVinci request app, the `pingone-promote` command-line tool and a GitLab CI request job. The DaVinci app files a request on a branch of the config repository; the CI job exports the source environment with a pinned pingcli-terraformer, neutralizes it, merges it additively into shared definitions plus one overlay per environment, and commits the result for review. Deployment stays in the enterprise Terraform pipeline; pingone-promote never plans or applies.

The Homebrew package installs the single `pingone-promote` binary with the subcommands `check --root`, `export` and `request`. It does not start a server or hold PingOne credentials. The earlier local web UI, the native Configuration Management API workflow (`pingone-runner`) and the Terraform-module translation path are retired; see `docs/adr/` (the old documents are in Git history).

This is an installation preview, not a production-readiness declaration. See `docs/acceptance/hosted-server.md` for the live acceptance cases and `docs/gitlab-ci-integration.md` for the CI contract.

## Whole environment and exclusions

A request can bring the whole source environment into Git (`scope: "all"` with `confirmAll: true`; the merge stays additive, nothing is deleted, and what differs from Git keeps its Git version unless you choose Update) and can leave resources out (`exclude`, and the dependency choice `exclude`). An excluded resource that Git does not manage is referenced by a per-environment ID variable (`<type>_<label>_id`) that every environment file must set. The DaVinci app has a Whole environment choice and an Exclude items step, with two new optional settings (`ppPathConnections`, `ppPathVariables`). See ADR 0015, ADR 0017 and `docs/gitlab-ci-integration.md`.
