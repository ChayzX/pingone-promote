# PingOne Promote preview

This preview is the v2 tool: a DaVinci request app, the `pingone-promote` command-line tool and a GitLab CI request job. The DaVinci app files a request on a branch of the config repository; the CI job exports the source environment with a pinned pingcli-terraformer, neutralizes it, merges it additively into shared definitions plus one overlay per environment, and commits the result for review. Deployment stays in the enterprise Terraform pipeline; pingone-promote never plans or applies.

The Homebrew package installs the single `pingone-promote` binary with the subcommands `check --root`, `export` and `request`. It does not start a server or hold PingOne credentials. The earlier local web UI, the native Configuration Management API workflow (`pingone-runner`) and the Terraform-module translation path are retired; see `docs/adr/` and `docs/archive/`.

This is an installation preview, not a production-readiness declaration. See `docs/acceptance/live-acceptance.md` for the live acceptance status and `docs/gitlab-ci-integration.md` for the CI contract.
