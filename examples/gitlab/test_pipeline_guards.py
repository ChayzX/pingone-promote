"""Execute guard commands from the GitLab template under Bash errexit."""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


TEMPLATE = Path(__file__).with_name(".gitlab-ci.yml")


def _script_entries(job_name: str) -> list[str]:
    """Read a job's script entries from the small YAML subset used here.

    The CI template has no YAML library requirement in this repo. This reader
    follows top-level job, script, sequence, and literal-block indentation and
    returns the actual command text for execution below.
    """
    lines = TEMPLATE.read_text(encoding="utf-8").splitlines()
    start = next(i for i, line in enumerate(lines) if line == f"{job_name}:") + 1
    end = next((i for i in range(start, len(lines)) if lines[i] and not lines[i][0].isspace()), len(lines))
    job = lines[start:end]
    script_at = next(i for i, line in enumerate(job) if line == "  script:")
    entries: list[str] = []
    i = script_at + 1
    while i < len(job):
        line = job[i]
        if line and not line.startswith("    "):
            break
        if line.startswith("    - |-") or line.startswith("    - |"):
            block_indent = 6
            i += 1
            block: list[str] = []
            while i < len(job):
                current = job[i]
                if current and len(current) - len(current.lstrip(" ")) < block_indent:
                    break
                block.append(current[block_indent:] if current else "")
                i += 1
            entries.append("\n".join(block).rstrip())
            continue
        if line.startswith("    - "):
            entries.append(line[6:])
        i += 1
    return entries


def _run_bash(script: str, env: dict[str, str], cwd: Path) -> subprocess.CompletedProcess[str]:
    bash = shutil.which("bash")
    if not bash:
        raise unittest.SkipTest("Bash is required to execute the GitLab guard scripts")
    merged = os.environ.copy()
    merged.update(env)
    return subprocess.run(
        [bash, "--noprofile", "--norc", "-euo", "pipefail", "-c", script],
        cwd=cwd,
        env=merged,
        text=True,
        capture_output=True,
        check=False,
    )


class PipelineGuardTests(unittest.TestCase):
    def test_apply_guards_stop_before_runner_on_missing_or_mismatched_trusted_settings(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            for job in ("apply_promotion", "apply_rollback"):
                entries = _script_entries(job)
                guards = [line for line in entries if line.startswith("test -n \"${PROMOTION_GITLAB_")]
                self.assertEqual(len(guards), 2, f"expected both URL and project guards in {job}")
                for label, env in (
                    ("missing URL", {"CI_SERVER_URL": "https://gitlab.example.test", "PROMOTION_GITLAB_URL": "", "CI_PROJECT_ID": "7", "PROMOTION_GITLAB_PROJECT_ID": "7", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main"}),
                    ("URL mismatch", {"CI_SERVER_URL": "https://gitlab.example.test", "PROMOTION_GITLAB_URL": "https://wrong.example.test", "CI_PROJECT_ID": "7", "PROMOTION_GITLAB_PROJECT_ID": "7", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main"}),
                    ("missing project", {"CI_SERVER_URL": "https://gitlab.example.test", "PROMOTION_GITLAB_URL": "https://gitlab.example.test", "CI_PROJECT_ID": "7", "PROMOTION_GITLAB_PROJECT_ID": "", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main"}),
                ):
                    marker = work / f"{job}-{label.replace(' ', '-')}.marker"
                    result = _run_bash("\n".join([*entries[:1], *guards, f"touch '{marker}'"]), env, work)
                    self.assertNotEqual(result.returncode, 0, f"{job} {label} unexpectedly succeeded")
                    self.assertFalse(marker.exists(), f"{job} continued to the runner marker after {label}")

    def test_rollback_plan_stops_before_artifact_fetch_on_invalid_server_or_project(self) -> None:
        entries = _script_entries("rollback_plan")
        self.assertEqual(len(entries), 1, "rollback_plan must keep its ordered credential/fetch script in one block")
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            bin_dir = work / "bin"
            bin_dir.mkdir()
            fetch_marker = work / "fetch.marker"
            curl = bin_dir / "curl"
            curl.write_text(f"#!/bin/sh\nprintf called > '{fetch_marker}'\nprintf 200\n", encoding="utf-8")
            curl.chmod(0o755)
            for label, env in (
                ("missing server", {"PROMOTION_GITLAB_URL": "", "CI_SERVER_URL": "https://gitlab.example.test", "CI_API_V4_URL": "/api/v4", "PROMOTION_GITLAB_PROJECT_ID": "7", "CI_PROJECT_ID": "7", "ORIGINAL_PLAN_JOB_ID": "41", "CI_JOB_TOKEN": "job-token"}),
                ("missing project", {"PROMOTION_GITLAB_URL": "https://gitlab.example.test", "CI_SERVER_URL": "https://gitlab.example.test", "CI_API_V4_URL": "https://gitlab.example.test/api/v4", "PROMOTION_GITLAB_PROJECT_ID": "", "CI_PROJECT_ID": "7", "ORIGINAL_PLAN_JOB_ID": "41", "CI_JOB_TOKEN": "job-token"}),
            ):
                fetch_marker.unlink(missing_ok=True)
                merged = {"PATH": str(bin_dir) + os.pathsep + os.environ.get("PATH", ""), **env}
                result = _run_bash(entries[0], merged, work)
                self.assertNotEqual(result.returncode, 0, f"rollback_plan {label} unexpectedly succeeded")
                self.assertFalse(fetch_marker.exists(), f"rollback_plan fetched credentials/artifacts after {label}")


if __name__ == "__main__":
    unittest.main()
