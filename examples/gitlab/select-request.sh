#!/usr/bin/env bash
# Source this file from a GitLab job. It selects exactly one new request or
# DaVinci intent, or checks an explicit manual-web request path; it never
# selects a glob or the latest file.
#
# Exports PROMOTION_REQUEST_FILE. For an intent (intents/<id>/intent.json) it
# also exports PROMOTION_INTENT_FILE, and PROMOTION_REQUEST_FILE names the
# request that `pingone-runner build-request` will write (built-request.json).
set -euo pipefail

fail() { printf '%s\n' "$1" >&2; exit 1; }
hex_request_id='[a-f0-9]{32}'

PROMOTION_INTENT_FILE=""
if [[ -n "${PROMOTION_REQUEST_FILE:-}" ]]; then
  [[ "${CI_PIPELINE_SOURCE:-}" == "web" ]] || fail "PROMOTION_REQUEST_FILE is only supported for a manual web pipeline"
  selected="$PROMOTION_REQUEST_FILE"
else
  if [[ "${CI_PIPELINE_SOURCE:-}" == "merge_request_event" ]]; then
    base="${CI_MERGE_REQUEST_DIFF_BASE_SHA:-}"
  else
    base="${CI_COMMIT_BEFORE_SHA:-}"
  fi
  [[ -n "$base" && "$base" != "0000000000000000000000000000000000000000" ]] || fail "Cannot determine the exact prior commit; refusing to select requests"
  added_file="$(mktemp)"
  if ! git diff --name-only -z --diff-filter=A "$base" "$CI_COMMIT_SHA" -- requests/ intents/ > "$added_file"; then
    rm -f -- "$added_file"
    fail "Could not compare request changes with the exact prior commit"
  fi
  mapfile -d '' -t added < "$added_file"
  rm -f -- "$added_file"
  [[ "${#added[@]}" -eq 1 ]] || fail "Expected exactly one newly added request.json or intent.json in this change; found ${#added[@]}"
  selected="${added[0]}"
fi

if [[ "${CI_PIPELINE_SOURCE:-}" != "web" && "$selected" =~ ^intents/${hex_request_id}/intent\.json$ ]]; then
  [[ ! -L intents && -d intents ]] || fail "The intents directory must be a real directory"
  intent_dir="${selected%/intent.json}"
  [[ ! -L "$intent_dir" && -d "$intent_dir" ]] || fail "The selected intent directory must be a real directory"
  [[ -f "$selected" && ! -L "$selected" ]] || fail "Selected intent must be a regular file, not a symlink"
  [[ ! -e built-request.json ]] || fail "built-request.json already exists; refusing to reuse it"
  PROMOTION_INTENT_FILE="$selected"
  PROMOTION_REQUEST_FILE="built-request.json"
  printf '%s\n' "$PROMOTION_INTENT_FILE" > selected-request.txt
  export PROMOTION_INTENT_FILE PROMOTION_REQUEST_FILE
  return 0 2>/dev/null || exit 0
fi

[[ "$selected" =~ ^requests/${hex_request_id}/request\.json$ ]] || fail "Request path must be requests/<32-character-lowercase-hex-id>/request.json or intents/<id>/intent.json"
[[ ! -L requests && -d requests ]] || fail "The requests directory must be a real directory"
request_dir="${selected%/request.json}"
[[ ! -L "$request_dir" && -d "$request_dir" ]] || fail "The selected request directory must be a real directory"
[[ -f "$selected" && ! -L "$selected" ]] || fail "Selected request must be a regular file, not a symlink"
PROMOTION_REQUEST_FILE="$selected"
printf '%s\n' "$PROMOTION_REQUEST_FILE" > selected-request.txt
export PROMOTION_INTENT_FILE PROMOTION_REQUEST_FILE
