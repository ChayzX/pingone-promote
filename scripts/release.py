#!/usr/bin/env python3
"""Build versioned release sources and a checksummed Homebrew formula.

Only Git objects enter the source archive. Local profiles, credentials, ignored
build output, and uncommitted files cannot be included accidentally.
"""
import argparse
import gzip
import hashlib
import io
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

REPOSITORY = "ChayzX/pingone-promote"
VERSION = re.compile(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?\Z")


def valid_version(value):
    if not VERSION.fullmatch(value):
        raise ValueError("version must be vMAJOR.MINOR.PATCH with an optional prerelease suffix")
    return value


def git(repo, *args):
    return subprocess.check_output(["git", "-C", str(repo), *args], stderr=subprocess.PIPE)


def source_archive(repo, ref, version, output):
    prefix = git(repo, "rev-parse", "--show-prefix").decode().strip().rstrip("/")
    tree = git(repo, "rev-parse", "--verify", ref + (":" + prefix if prefix else "^{tree}")).decode().strip()
    top = Path(git(repo, "rev-parse", "--show-toplevel").decode().strip())
    raw = git(top, "archive", "--format=tar", tree)
    package = f"pingone-promote-{version}"
    path = output / f"{package}-source.tar.gz"
    # Normalize Git archive metadata so exporting the same tree is byte-identical.
    with path.open("wb") as target, gzip.GzipFile(filename="", fileobj=target, mode="wb", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.PAX_FORMAT) as archive, tarfile.open(fileobj=io.BytesIO(raw)) as original:
            for member in original.getmembers():
                member.name = package + "/" + member.name
                member.uid = member.gid = member.mtime = 0
                member.uname = member.gname = ""
                member.pax_headers = {}
                archive.addfile(member, original.extractfile(member) if member.isfile() else None)
    return path


def formula(version, archive, local=False):
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    url = archive.resolve().as_uri() if local else f"https://github.com/{REPOSITORY}/releases/download/{version}/{archive.name}"
    return '''class PingonePromote < Formula
  desc "Review selected PingOne configuration and submit GitLab promotion requests"
  homepage "https://github.com/ChayzX/pingone-promote"
  url "@URL@"
  version "@VERSION@"
  sha256 "@SHA@"

  depends_on "go" => :build

  def install
    ENV["CGO_ENABLED"] = "0"
    ENV["GOTOOLCHAIN"] = "local"
    ldflags = "-X pingone-promote/internal/buildinfo.Version=v#{version}"
    system "go", "build", *std_go_args(output: bin/"pingone-promote", ldflags:), "./cmd/pingone-promote"
    system "go", "build", *std_go_args(output: bin/"pingone-runner", ldflags:), "./cmd/pingone-runner"
    doc.install "README.md"
    pkgshare.install "docs", "examples"
  end

  def caveats
    <<~EOS
      Preview release. Native PingOne promotion and rollback still need live acceptance.
      Run pingone-promote to open the local browser interface.
      Profiles and history are stored outside Homebrew and are preserved on uninstall.
      Production planning, deployment and rollback remain controlled by GitLab.
    EOS
  end

  test do
    assert_equal "pingone-promote v#{version}\\n", shell_output("#{bin}/pingone-promote --version")
    assert_equal "pingone-runner v#{version}\\n", shell_output("#{bin}/pingone-runner --version")
    (testpath/"request.json").write("{}")
    assert_match "version", shell_output("#{bin}/pingone-runner validate --request #{testpath}/request.json 2>&1", 1)
  end
end
'''.replace("@URL@", url).replace("@VERSION@", version[1:]).replace("@SHA@", digest)


def checksums(directory):
    paths = sorted(path for path in directory.iterdir() if path.is_file() and path.name != "SHA256SUMS")
    (directory / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in paths), encoding="utf-8", newline="\n")


def verify(directory):
    lines = (directory / "SHA256SUMS").read_text().splitlines()
    if not lines:
        raise ValueError("checksum manifest is empty")
    seen = set()
    for line in lines:
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\\r\n]+)", line)
        if not match or match[2] in (".", "..", "SHA256SUMS") or match[2] in seen:
            raise ValueError("invalid or duplicate checksum filename")
        digest, name = match.groups()
        path = directory / name
        if path.is_symlink() or path.resolve().parent != directory.resolve():
            raise ValueError("invalid checksum filename")
        if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise ValueError(f"checksum mismatch: {name}")
        seen.add(name)
    actual = {path.name for path in directory.iterdir() if path.is_file() and path.name != "SHA256SUMS"}
    if actual != seen:
        raise ValueError("release contains files missing from checksum manifest")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("source", "release"):
        command = commands.add_parser(name)
        command.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[1])
        command.add_argument("--ref", default="HEAD", help="Git commit or tree to package; never the working directory")
        command.add_argument("--version", required=True)
        command.add_argument("--output", type=Path, required=True)
    command = commands.add_parser("formula")
    command.add_argument("--version", required=True)
    command.add_argument("--archive", type=Path, required=True)
    command.add_argument("--output", type=Path, required=True)
    command.add_argument("--local-source", action="store_true", help="use this local archive for installation tests only")
    command = commands.add_parser("verify")
    command.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "verify":
        verify(args.directory)
        print("Release checksums verified")
        return
    version = valid_version(args.version)
    if args.command == "formula":
        expected = f"pingone-promote-{version}-source.tar.gz"
        if args.archive.name != expected:
            raise ValueError(f"source archive must be named {expected}")
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(formula(version, args.archive, args.local_source), encoding="utf-8", newline="\n")
        return
    if args.output.exists() and any(args.output.iterdir()):
        raise ValueError("release output must be empty; existing artifacts will not be overwritten")
    args.output.mkdir(parents=True, exist_ok=True)
    archive = source_archive(args.repo.resolve(), args.ref, version, args.output)
    (args.output / "pingone-promote.rb").write_text(formula(version, archive), encoding="utf-8", newline="\n")
    if args.command == "release":
        with tempfile.TemporaryDirectory(prefix="pingone-release-") as temporary:
            with tarfile.open(archive) as source:
                source.extractall(temporary, filter="data")
            root = Path(temporary) / f"pingone-promote-{version}"
            subprocess.run(["sh", "scripts/build.sh"], cwd=root, env={**os.environ, "VERSION": version}, check=True)
            for target in ("darwin-arm64", "darwin-amd64", "linux-amd64"):
                shutil.copyfile(root / "dist" / f"pingone-promote-{target}.tar.gz", args.output / f"pingone-promote-{version}-{target}.tar.gz")
    checksums(args.output)
    verify(args.output)
    print(f"Created {version} release assets in {args.output}")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, tarfile.TarError) as error:
        print(f"Release failed: {error}", file=sys.stderr)
        sys.exit(1)
