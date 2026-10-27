"""Regression checks for the GitLab request selector (Python stdlib only)."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("select-request.sh").resolve()
ID_A = "a" * 32
ID_B = "b" * 32


class SelectorRepo:
    def __init__(self, root: Path):
        self.root = root
        self.git("init", "-q")
        self.git("config", "user.email", "selector-test@example.invalid")
        self.git("config", "user.name", "selector test")
        (root / "README.md").write_text("base\n")
        self.git("add", "README.md")
        self.git("commit", "-qm", "base")
        self.base = self.git("rev-parse", "HEAD").stdout.strip()

    def git(self, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["git", *args], cwd=self.root, check=True, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )

    def add_request(self, request_id: str) -> None:
        path = self.root / "requests" / request_id / "request.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('{"fixture":true}\n')
        self.git("add", str(path.relative_to(self.root)))

    def commit(self) -> str:
        self.git("commit", "-qm", "request")
        return self.git("rev-parse", "HEAD").stdout.strip()

    def run_selector(self, *, source: str, commit: str, base: str = "", explicit: str = ""):
        env = os.environ.copy()
        env.update({"CI_PIPELINE_SOURCE": source, "CI_COMMIT_SHA": commit})
        env["CI_COMMIT_BEFORE_SHA"] = base
        env["CI_MERGE_REQUEST_DIFF_BASE_SHA"] = base
        env["PROMOTION_REQUEST_FILE"] = explicit
        return subprocess.run(
            ["bash", "-c", 'source "$1" && printf "%s\\n" "$PROMOTION_REQUEST_FILE"', "selector-test", str(SCRIPT)],
            cwd=self.root, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )


class SelectRequestTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.repo = SelectorRepo(Path(self.temp.name))

    def tearDown(self):
        self.temp.cleanup()

    def test_merge_request_selects_single_added_request(self):
        self.repo.add_request(ID_A)
        commit = self.repo.commit()
        result = self.repo.run_selector(source="merge_request_event", commit=commit, base=self.repo.base)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), f"requests/{ID_A}/request.json")

    def test_push_selects_single_added_request(self):
        self.repo.add_request(ID_A)
        commit = self.repo.commit()
        result = self.repo.run_selector(source="push", commit=commit, base=self.repo.base)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), f"requests/{ID_A}/request.json")

    def test_manual_web_pipeline_accepts_explicit_safe_path(self):
        self.repo.add_request(ID_A)
        commit = self.repo.commit()
        result = self.repo.run_selector(source="web", commit=commit, explicit=f"requests/{ID_A}/request.json")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), f"requests/{ID_A}/request.json")

    def test_zero_added_requests_fails(self):
        commit = self.repo.git("rev-parse", "HEAD").stdout.strip()
        result = self.repo.run_selector(source="push", commit=commit, base=self.repo.base)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("found 0", result.stderr)

    def test_multiple_added_requests_fail(self):
        self.repo.add_request(ID_A)
        self.repo.add_request(ID_B)
        commit = self.repo.commit()
        result = self.repo.run_selector(source="push", commit=commit, base=self.repo.base)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("found 2", result.stderr)

    def test_malicious_manual_path_fails(self):
        (self.repo.root / "request.json").write_text("{}\n")
        commit = self.repo.git("rev-parse", "HEAD").stdout.strip()
        result = self.repo.run_selector(source="web", commit=commit, explicit="requests/../request.json")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Request path must be", result.stderr)

    def test_symlinked_request_file_fails(self):
        directory = self.repo.root / "requests" / ID_A
        directory.mkdir(parents=True)
        target = self.repo.root / "README.md"
        (directory / "request.json").symlink_to(target)
        commit = self.repo.git("rev-parse", "HEAD").stdout.strip()
        result = self.repo.run_selector(source="web", commit=commit, explicit=f"requests/{ID_A}/request.json")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("regular file", result.stderr)

    def test_symlinked_request_directory_fails(self):
        requests = self.repo.root / "requests"
        requests.mkdir()
        (requests / ID_A).symlink_to(self.repo.root, target_is_directory=True)
        commit = self.repo.git("rev-parse", "HEAD").stdout.strip()
        result = self.repo.run_selector(source="web", commit=commit, explicit=f"requests/{ID_A}/request.json")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("request directory", result.stderr)

    def test_invalid_base_commit_fails_closed(self):
        self.repo.add_request(ID_A)
        commit = self.repo.commit()
        result = self.repo.run_selector(source="push", commit=commit, base="not-a-commit")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exact prior commit", result.stderr)

    def test_all_zero_base_fails_closed(self):
        commit = self.repo.git("rev-parse", "HEAD").stdout.strip()
        result = self.repo.run_selector(source="push", commit=commit, base="0" * 40)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exact prior commit", result.stderr)


if __name__ == "__main__":
    unittest.main()
