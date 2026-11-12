"""Regression tests for the verbose Go test skip guard."""
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts/check_test_skips.py"


class SkipGuardTests(unittest.TestCase):
    def check(self, log):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "test.log"
            path.write_text(log, encoding="utf-8")
            return subprocess.run(
                [sys.executable, str(CHECKER), str(path)],
                capture_output=True,
                text=True,
            )

    def test_old_grep_policy_rejects_allowed_skip_followed_by_next_run(self):
        log = (
            "=== RUN   TestRegenerateRepoFixture\n"
            "    harness_test.go:158: set PINGONE_PROMOTE_REGEN_FIXTURE=1 to rebuild testdata/repo\n"
            "--- SKIP: TestRegenerateRepoFixture (0.00s)\n"
            "=== RUN   TestReferenceRootIsCurrent\n"
            "--- PASS: TestReferenceRootIsCurrent (0.00s)\n"
        )
        old_guard = subprocess.run(
            ["bash", "-c", "grep -E -A1 -- '--- SKIP' \"$1\" | grep -v -E 'Windows|REGEN_FIXTURE|--- SKIP|^--$'", "_", "/dev/stdin"],
            input=log,
            capture_output=True,
            text=True,
        )
        self.assertEqual(old_guard.returncode, 0, old_guard.stderr)
        self.assertIn("=== RUN   TestReferenceRootIsCurrent", old_guard.stdout)

    def test_allows_only_exact_fixture_regen_skip_without_running_regeneration(self):
        result = self.check(
            "=== RUN   TestRegenerateRepoFixture\n"
            "    harness_test.go:158: set PINGONE_PROMOTE_REGEN_FIXTURE=1 to rebuild testdata/repo\n"
            "--- SKIP: TestRegenerateRepoFixture (0.00s)\n"
            "=== RUN   TestReferenceRootIsCurrent\n"
            "--- PASS: TestReferenceRootIsCurrent (0.00s)\n",
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_fixture_skip_without_regen_reason(self):
        result = self.check(
            "=== RUN   TestRegenerateRepoFixture\n"
            "    harness_test.go:158: unrelated skip\n"
            "--- SKIP: TestRegenerateRepoFixture (0.00s)\n",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected skipped test", result.stderr)

    def test_rejects_unexpected_skip(self):
        result = self.check("=== RUN   TestNeedsTerraform\n--- SKIP: TestNeedsTerraform (0.00s)\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected skipped test", result.stderr)

    def test_rejects_suffix_that_looks_like_allowed_name(self):
        result = self.check(
            "=== RUN   TestRegenerateRepoFixtureExtra\n"
            "    set PINGONE_PROMOTE_REGEN_FIXTURE=1\n"
            "--- SKIP: TestRegenerateRepoFixtureExtra (0.00s)\n",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected skipped test", result.stderr)

    def test_rejects_skipped_subtest_even_after_parent_passes(self):
        result = self.check(
            "=== RUN   TestReferenceRootIsCurrent\n"
            "--- PASS: TestReferenceRootIsCurrent (0.00s)\n"
            "=== RUN   TestRegenerateRepoFixture/subcase\n"
            "    --- SKIP: TestRegenerateRepoFixture/subcase (0.00s)\n",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected skipped test", result.stderr)

    def test_rejects_indented_regen_subtest_skip_after_allowed_parent_skip(self):
        result = self.check(
            "=== RUN   TestRegenerateRepoFixture\n"
            "    harness_test.go:158: set PINGONE_PROMOTE_REGEN_FIXTURE=1 to rebuild testdata/repo\n"
            "--- SKIP: TestRegenerateRepoFixture (0.00s)\n"
            "=== RUN   TestRegenerateRepoFixture/subcase\n"
            "    --- SKIP: TestRegenerateRepoFixture/subcase (0.00s)\n",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected skipped test: TestRegenerateRepoFixture/subcase", result.stderr)

    def test_accepts_log_with_no_skips(self):
        result = self.check("=== RUN   TestWorks\n--- PASS: TestWorks (0.00s)\n")
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
