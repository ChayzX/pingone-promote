"""Exercise release artifacts, not just their build configuration."""
import hashlib
import io
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import unittest
import sys

ROOT = Path(__file__).resolve().parents[1]


class SourceReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True)
        for name in ("go.mod", "cmd/pingone-promote/main.go", "cmd/pingone-runner/main.go", "README.md"):
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("tracked source\n", encoding="utf-8")
        subprocess.run(["git", "add", "."], cwd=self.repo, check=True)
        subprocess.run(["git", "-c", "user.name=Package fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "fixture"], cwd=self.repo, check=True)
        (self.repo / "profiles.json").write_text("PRIVATE_FIXTURE_NOT_FOR_ARCHIVE")

    def command(self, *args):
        return subprocess.run([sys.executable, str(ROOT / "scripts/release.py"), *args], capture_output=True, text=True)

    def test_source_archive_formula_and_checksums(self):
        output = self.root / "release"
        result = self.command("source", "--repo", str(self.repo), "--ref", "HEAD", "--version", "v0.2.0-rc.1", "--output", str(output))
        self.assertEqual(result.returncode, 0, result.stderr)
        source = output / "pingone-promote-v0.2.0-rc.1-source.tar.gz"
        with tarfile.open(source) as archive:
            names = archive.getnames()
            self.assertIn("pingone-promote-v0.2.0-rc.1/cmd/pingone-runner/main.go", names)
            self.assertFalse(any("profiles.json" in name or "/.git/" in name for name in names))
        formula = (output / "pingone-promote.rb").read_text()
        self.assertIn(hashlib.sha256(source.read_bytes()).hexdigest(), formula)
        self.assertIn("/releases/download/v0.2.0-rc.1/pingone-promote-v0.2.0-rc.1-source.tar.gz", formula)
        self.assertIn('version "0.2.0-rc.1"', formula)
        self.assertIn('bin/"pingone-promote"', formula)
        self.assertNotIn('bin/"pingone-runner"', formula)
        self.assertEqual(self.command("verify", "--directory", str(output)).returncode, 0)
        source.write_bytes(source.read_bytes() + b"tampered")
        bad = self.command("verify", "--directory", str(output))
        self.assertNotEqual(bad.returncode, 0)
        self.assertIn("checksum mismatch", bad.stderr.lower())

    def test_repeated_source_export_is_byte_identical(self):
        outputs = [self.root / name for name in ("one", "two")]
        for output in outputs:
            result = self.command("source", "--repo", str(self.repo), "--ref", "HEAD", "--version", "v0.2.0-rc.1", "--output", str(output))
            self.assertEqual(result.returncode, 0, result.stderr)
        for name in ("pingone-promote-v0.2.0-rc.1-source.tar.gz", "SHA256SUMS", "pingone-promote.rb"):
            self.assertEqual((outputs[0] / name).read_bytes(), (outputs[1] / name).read_bytes())

    def test_source_archive_from_module_subdirectory_contains_source(self):
        module = self.repo / "tool"
        module.mkdir()
        self.assertTrue(module.resolve().is_relative_to(self.repo.resolve()))
        subprocess.run(["git", "mv", "go.mod", "cmd", "README.md", "tool/"], cwd=self.repo, check=True)
        subprocess.run(["git", "-c", "user.name=Package fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "nest module"], cwd=self.repo, check=True)
        output = self.root / "nested-release"
        result = self.command("source", "--repo", str(module), "--ref", "HEAD", "--version", "v0.2.0-rc.1", "--output", str(output))
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(output / "pingone-promote-v0.2.0-rc.1-source.tar.gz") as archive:
            self.assertIn("pingone-promote-v0.2.0-rc.1/go.mod", archive.getnames())

    def test_invalid_version_cannot_create_release(self):
        for version in ("latest", "v1.2", "v1.2.3\nanything", "v1.2.3;echo", "../v1.2.3"):
            output = self.root / "invalid-output"
            result = self.command("source", "--repo", str(self.repo), "--version", version, "--output", str(output))
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("version", result.stderr.lower())
            self.assertFalse(output.exists())

    def test_verification_does_not_follow_manifest_paths(self):
        output = self.root / "unsafe"
        output.mkdir()
        outside = self.root / "outside"
        outside.write_text("outside")
        (output / "SHA256SUMS").write_text(hashlib.sha256(outside.read_bytes()).hexdigest() + "  ../outside\n")
        result = self.command("verify", "--directory", str(output))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("filename", result.stderr.lower())

    def test_existing_release_output_is_not_overwritten(self):
        output = self.root / "existing"
        output.mkdir()
        marker = output / "keep"
        marker.write_text("preserve")
        result = self.command("source", "--repo", str(self.repo), "--version", "v0.2.0-rc.1", "--output", str(output))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(marker.read_text(), "preserve")


class BuildMetadataTests(unittest.TestCase):
    def test_build_stamps_version_and_verifiable_archives(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            names = subprocess.check_output(["git", "ls-files", "--", "."], cwd=ROOT, text=True).splitlines()
            for name in names:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(ROOT / name, target)
            (root / "profiles.json").write_text('{"private":"NOT_FOR_DISTRIBUTION"}')
            version = "v0.2.0-rc.1"
            subprocess.run(["sh", "scripts/build.sh"], cwd=root, env={**os.environ, "VERSION": version}, check=True, capture_output=True, text=True)
            arch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine()]
            system = {"Linux": "linux", "Darwin": "darwin"}[platform.system()]
            for command in ("pingone-promote", "pingone-runner"):
                config = root / "must-not-create"
                result = subprocess.run([str(root / "dist" / f"{command}-{system}-{arch}"), "--version"], env={**os.environ, "XDG_CONFIG_HOME": str(config)}, check=True, capture_output=True, text=True)
                self.assertEqual(result.stdout, f"{command} {version}\n")
                self.assertFalse(config.exists(), "version reporting created app data")
            archives = sorted((root / "dist").glob("*.tar.gz"))
            self.assertEqual(len(archives), 3)
            sums = {}
            for line in (root / "dist/SHA256SUMS").read_text().splitlines():
                digest, name = line.split("  ", 1)
                sums[name] = digest
            self.assertEqual(set(sums), {archive.name for archive in archives})
            for archive in archives:
                self.assertEqual(sums[archive.name], hashlib.sha256(archive.read_bytes()).hexdigest())
                with tarfile.open(archive) as bundle:
                    members = bundle.getnames()
                    self.assertTrue(any(name.startswith("pingone-promote-") for name in members))
                    self.assertTrue(any(name.startswith("pingone-runner-") for name in members))
                    self.assertIn("docs/gitlab.md", members)
                    self.assertIn("examples/gitlab/.gitlab-ci.yml", members)
                    self.assertFalse(any(name.endswith("profiles.json") or "/.git/" in name for name in members))


if __name__ == "__main__":
    unittest.main()
