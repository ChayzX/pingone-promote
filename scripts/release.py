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


def git(repo, *args, **kwargs):
    return subprocess.check_output(["git", "-C", str(repo), *args], stderr=subprocess.PIPE, **kwargs)


def git_path(repo, *args):
    # Git ends path output with one newline; keep any other whitespace and
    # decode the bytes the way the filesystem does.
    return os.fsdecode(git(repo, *args).removesuffix(b"\n"))


def tree_members(repo, tree):
    """Yield (path, mode, type, object) for every entry of a Git tree.

    The archive is built from Git objects directly rather than with
    'git archive', so local settings (tar.umask, core.autocrlf,
    .git/info/attributes, core.attributesFile) cannot change its bytes.
    """
    listing = git(repo, "ls-tree", "-r", "-t", "-z", "--full-tree", tree)
    for entry in listing.split(b"\0"):
        if not entry:
            continue
        meta, path = entry.split(b"\t", 1)
        mode, kind, obj = meta.decode().split()
        yield os.fsdecode(path), mode, kind, obj


def source_archive(repo, ref, version, output):
    prefix = git_path(repo, "rev-parse", "--show-prefix").rstrip("/")
    tree = git(repo, "rev-parse", "--verify", ref + (":" + prefix if prefix else "^{tree}")).decode().strip()
    package = f"pingone-promote-{version}"
    path = output / f"{package}-source.tar.gz"
    # Fix every piece of metadata so exporting the same tree is byte-identical
    # on any machine.
    with path.open("wb") as target, gzip.GzipFile(filename="", fileobj=target, mode="wb", mtime=0) as compressed:
        with tarfile.open(fileobj=compressed, mode="w|", format=tarfile.PAX_FORMAT) as archive:
            for name, mode, kind, obj in tree_members(repo, tree):
                member = tarfile.TarInfo(package + "/" + name)
                member.uid = member.gid = member.mtime = 0
                member.uname = member.gname = ""
                if kind == "tree":
                    member.type, member.mode = tarfile.DIRTYPE, 0o755
                    archive.addfile(member)
                elif kind == "blob" and mode in ("100644", "100755"):
                    data = git(repo, "cat-file", "blob", obj)
                    member.mode = 0o755 if mode == "100755" else 0o644
                    member.size = len(data)
                    archive.addfile(member, io.BytesIO(data))
                elif kind == "blob" and mode == "120000":
                    member.type, member.mode = tarfile.SYMTYPE, 0o777
                    member.linkname = os.fsdecode(git(repo, "cat-file", "blob", obj))
                    archive.addfile(member)
                else:
                    raise ValueError(f"unsupported Git tree entry {mode} {kind} for {name}")
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
    doc.install "README.md"
    pkgshare.install "docs", "examples"
  end

  def caveats
    <<~EOS
      Preview release. pingone-promote is a command-line tool with the
      subcommands check, export, request and serve. The request subcommand
      runs in a GitLab CI job (see docs/gitlab-ci-integration.md); serve runs
      the hosted server and is not started by this package.
      Deployment stays in the enterprise Terraform pipeline.
    EOS
  end

  test do
    assert_equal "pingone-promote v#{version}\\n", shell_output("#{bin}/pingone-promote --version")
  end
end
'''.replace("@URL@", url).replace("@VERSION@", version[1:]).replace("@SHA@", digest)


def checksums(directory):
    paths = sorted(path for path in directory.iterdir() if path.is_file() and path.name != "SHA256SUMS")
    (directory / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in paths), encoding="utf-8", newline="\n")


TARGETS = ("darwin-arm64", "darwin-amd64", "linux-amd64")


def expected_files(version, kind):
    names = {f"pingone-promote-{version}-source.tar.gz", "pingone-promote.rb"}
    if kind == "release":
        names |= {f"pingone-promote-{version}-{target}.tar.gz" for target in TARGETS}
    return names


def verify(directory, version=None, kind="source"):
    lines = (directory / "SHA256SUMS").read_text(encoding="utf-8").splitlines()
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
    actual = set()
    for path in directory.iterdir():
        if path.name == "SHA256SUMS":
            continue
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"release contains an entry that is not a regular file: {path.name}")
        actual.add(path.name)
    if actual != seen:
        raise ValueError("release contains files missing from checksum manifest")
    if version is not None and seen != expected_files(valid_version(version), kind):
        raise ValueError(f"release does not contain exactly the expected {kind} artifacts for {version}")


def default_umask():
    mask = os.umask(0o022)
    os.umask(mask)
    return mask


def build_release(args, version, staging):
    archive = source_archive(args.repo.resolve(), args.ref, version, staging)
    (staging / "pingone-promote.rb").write_text(formula(version, archive), encoding="utf-8", newline="\n")
    if args.command == "release":
        with tempfile.TemporaryDirectory(prefix="pingone-release-") as temporary:
            with tarfile.open(archive) as source:
                source.extractall(temporary, filter="data")
            root = Path(temporary) / f"pingone-promote-{version}"
            subprocess.run(["sh", "scripts/build.sh"], cwd=root, env={**os.environ, "VERSION": version}, check=True)
            for target in TARGETS:
                shutil.copyfile(root / "dist" / f"pingone-promote-{target}.tar.gz", staging / f"pingone-promote-{version}-{target}.tar.gz")
    checksums(staging)
    verify(staging, version, args.command)


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
    command.add_argument("--version", help="also require exactly the artifacts of this version")
    command.add_argument("--kind", choices=("source", "release"), default="release", help="artifact set required with --version (default: release)")
    args = parser.parse_args()
    if args.command == "verify":
        verify(args.directory, args.version, args.kind)
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
    output = args.output.absolute()
    if output.exists() and any(output.iterdir()):
        raise ValueError("release output must be empty; existing artifacts will not be overwritten")
    output.parent.mkdir(parents=True, exist_ok=True)
    # Build and verify in a sibling directory, then move it into place, so a
    # failed run leaves the output untouched and can simply be retried.
    staging = Path(tempfile.mkdtemp(prefix=f".{output.name}-staging-", dir=output.parent))
    try:
        build_release(args, version, staging)
        os.chmod(staging, 0o777 & ~default_umask())
        # rename() replaces an empty directory but refuses a nonempty one, so
        # artifacts that appeared meanwhile are never overwritten.
        os.rename(staging, output)
    except BaseException:
        shutil.rmtree(staging, ignore_errors=True)
        raise
    print(f"Created {version} release assets in {output}")

if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, tarfile.TarError) as error:
        print(f"Release failed: {error}", file=sys.stderr)
        sys.exit(1)
