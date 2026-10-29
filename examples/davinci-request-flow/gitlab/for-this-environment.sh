#!/usr/bin/env bash
# Source after examples/gitlab/select-request.sh in a job that runs once per
# GitLab environment (dev, test, production ...) of the one promotion project.
#
# Sets PP_FOR_THIS_ENVIRONMENT=true only when the selected request's target is
# this environment's trusted PINGONE_TARGET_ENVIRONMENT_ID (an
# environment-scoped CI/CD variable). Jobs of other environments then stop
# without touching PingOne. The runner re-checks the target itself, so a
# mistake here fails closed rather than deploying to the wrong place.
set -euo pipefail

pp_fail() { printf '%s\n' "$1" >&2; exit 1; }

[[ -n "${CI_ENVIRONMENT_NAME:-}" ]] || pp_fail "This job must declare a GitLab environment"
# The trusted environment name: an environment-scoped PROMOTION_ENVIRONMENT
# must agree with the job's environment; unset means the job's environment.
PROMOTION_ENVIRONMENT="${PROMOTION_ENVIRONMENT:-$CI_ENVIRONMENT_NAME}"
[[ "$PROMOTION_ENVIRONMENT" == "$CI_ENVIRONMENT_NAME" ]] ||
  pp_fail "PROMOTION_ENVIRONMENT ($PROMOTION_ENVIRONMENT) differs from this job's GitLab environment ($CI_ENVIRONMENT_NAME); scope it to $CI_ENVIRONMENT_NAME"
export PROMOTION_ENVIRONMENT
[[ -n "${PINGONE_TARGET_ENVIRONMENT_ID:-}" ]] ||
  pp_fail "PINGONE_TARGET_ENVIRONMENT_ID is not set for GitLab environment $CI_ENVIRONMENT_NAME (scope it to that environment)"

pp_file="${PROMOTION_INTENT_FILE:-}"
[[ -n "$pp_file" ]] || pp_file="${PROMOTION_REQUEST_FILE:-}"
[[ -n "$pp_file" && -f "$pp_file" && ! -L "$pp_file" ]] || pp_fail "No selected request file"
# request.json and intent.json both put the target's "id" first.
pp_target="$(tr -d '\n\r' < "$pp_file" | grep -oE '"target": *\{ *"id": *"[A-Za-z0-9_-]+"' | head -n 1 | sed -E 's/.*"([A-Za-z0-9_-]+)"$/\1/')" || true
[[ -n "$pp_target" ]] || pp_fail "Could not read the target environment from $pp_file"

if [[ "$pp_target" == "$PINGONE_TARGET_ENVIRONMENT_ID" ]]; then
  PP_FOR_THIS_ENVIRONMENT=true
  printf 'This request targets %s, which GitLab environment %s deploys to.\n' "$pp_target" "$CI_ENVIRONMENT_NAME"
else
  PP_FOR_THIS_ENVIRONMENT=false
  printf 'Not for %s: this request targets %s. Nothing to do in this job; use the jobs of the environment that deploys to it.\n' \
    "$CI_ENVIRONMENT_NAME" "$pp_target"
fi
export PP_FOR_THIS_ENVIRONMENT
