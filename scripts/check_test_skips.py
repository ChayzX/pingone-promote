"""Reject skipped Go tests outside the explicit fixture regeneration case."""
from pathlib import Path
import re
import sys

SKIP = re.compile(r"^\s*--- SKIP: ([^\s]+)(?:\s|$)")
REGEN_TEST = "TestRegenerateRepoFixture"
REGEN_REASON = "set PINGONE_PROMOTE_REGEN_FIXTURE=1 to rebuild testdata/repo"


def unexpected_skips(lines):
    current_test = None
    saw_regen_reason = False
    failures = []
    for line in lines:
        if line.startswith("=== RUN   "):
            current_test = line[len("=== RUN   "):].strip()
            saw_regen_reason = False
        elif current_test == REGEN_TEST and REGEN_REASON in line:
            saw_regen_reason = True

        match = SKIP.match(line)
        if not match:
            continue
        skipped_test = match.group(1)
        allowed = (
            skipped_test == REGEN_TEST
            and current_test == REGEN_TEST
            and saw_regen_reason
        )
        if not allowed:
            failures.append(skipped_test)
        saw_regen_reason = False
    return failures


def main(argv=None):
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        print(f"usage: {Path(sys.argv[0]).name} GO_TEST_LOG", file=sys.stderr)
        return 2
    try:
        lines = Path(args[0]).read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        print(f"cannot read Go test log: {exc}", file=sys.stderr)
        return 2

    failures = unexpected_skips(lines)
    if failures:
        for test in failures:
            print(f"unexpected skipped test: {test}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
