#!/usr/bin/env python3
"""Install the repository's pinned hosted-service tools without invoking a shell."""

import argparse
import hashlib
import io
import json
import os
import pathlib
import platform
import re
import stat
import sys
import tempfile
import urllib.request
import zipfile


class InstallError(Exception):
    pass


MAX_ARCHIVE = 512 * 1024 * 1024
DIGEST = re.compile(r"^[0-9a-f]{64}$")


def platform_key(system, machine):
    systems = {"darwin": "darwin", "linux": "linux"}
    machines = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
    try:
        return systems[system.lower()] + "_" + machines[machine.lower()]
    except KeyError:
        raise InstallError("supported platforms are Darwin/Linux on amd64/arm64")


def fetch_archive(url):
    if not url.startswith("https://"):
        raise InstallError("tool lock URLs must use HTTPS")
    request = urllib.request.Request(url, headers={"User-Agent": "pingone-promote-tool-installer"})
    with urllib.request.urlopen(request, timeout=45) as response:
        data = response.read(MAX_ARCHIVE + 1)
    if len(data) > MAX_ARCHIVE:
        raise InstallError("download exceeds the 512 MiB limit")
    return data


def checked_member(data, expected):
    try:
        archive = zipfile.ZipFile(io.BytesIO(data))
    except (zipfile.BadZipFile, OSError) as exc:
        raise InstallError("download is not a valid ZIP archive") from exc
    with archive:
        matches = []
        for info in archive.infolist():
            name = info.filename
            path = pathlib.PurePosixPath(name)
            mode = info.external_attr >> 16
            if path.is_absolute() or ".." in path.parts or "\\" in name:
                raise InstallError("ZIP contains an unsafe path")
            if stat.S_ISLNK(mode):
                raise InstallError("ZIP contains a symbolic link")
            if info.file_size > MAX_ARCHIVE:
                raise InstallError("ZIP member exceeds the 512 MiB limit")
            if info.is_dir():
                continue
            if name == expected or path.name == expected:
                matches.append(info)
        if len(matches) != 1:
            raise InstallError("ZIP does not contain exactly one expected executable")
        try:
            return archive.read(matches[0])
        except (RuntimeError, zipfile.BadZipFile) as exc:
            raise InstallError("cannot read executable from ZIP") from exc


def install(lock_path, system, machine, bin_dir, cache_dir):
    key = platform_key(system, machine)
    try:
        lock = json.loads(pathlib.Path(lock_path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise InstallError("cannot read tools lock file") from exc
    if key not in lock.get("platforms", []):
        raise InstallError("platform is not pinned in tools.lock.json")
    bin_dir = pathlib.Path(bin_dir)
    cache_dir = pathlib.Path(cache_dir)
    bin_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    cache_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(bin_dir, 0o700)
    os.chmod(cache_dir, 0o700)

    executables = {}
    for tool_name in ("terraform", "terraformer", "pingcli"):
        spec = lock[tool_name]
        archive_name = spec["archive"].format(platform=key)
        payload = _download_checked(spec, key, archive_name)
        binary = checked_member(payload, spec["binary"]) if archive_name.endswith(".zip") else payload
        executable = bin_dir / spec["binary"]
        _atomic_write(executable, binary, 0o500)
        executables[tool_name] = {
            "path": str(executable.resolve()),
            "sha256": hashlib.sha256(binary).hexdigest(),
            "version": spec["version"],
        }

    provider = lock["provider"]
    provider_name = provider["archive"].format(platform=key)
    provider_payload = _download_checked(provider, key, provider_name)
    # The importer consumes this ZIP later. Validate its table and retain the
    # verified archive intact so an offline mirror can use the same bytes.
    _validate_zip(provider_payload)
    provider_path = cache_dir / provider_name
    _atomic_write(provider_path, provider_payload, 0o400)

    return {
        "tools": {
            "terraform": executables["terraform"]["path"],
            "terraformVersion": executables["terraform"]["version"],
            "terraformSha256": executables["terraform"]["sha256"],
            "terraformer": executables["terraformer"]["path"],
            "terraformerVersion": executables["terraformer"]["version"],
            "terraformerSha256": executables["terraformer"]["sha256"],
            "pingcli": executables["pingcli"]["path"],
            "pingcliVersion": executables["pingcli"]["version"],
            "pingcliSha256": executables["pingcli"]["sha256"],
            "providerZip": str(provider_path.resolve()),
            "providerSha256": hashlib.sha256(provider_payload).hexdigest(),
            "providerVersion": provider["version"],
        }
    }


def _download_checked(spec, key, archive_name):
    digest = spec.get("sha256", {}).get(key, "")
    if not DIGEST.fullmatch(digest):
        raise InstallError("tools lock has no valid SHA-256 for " + key)
    data = fetch_archive(spec["source"] + archive_name)
    if hashlib.sha256(data).hexdigest() != digest:
        raise InstallError("download checksum does not match tools.lock.json: " + archive_name)
    return data


def _validate_zip(data):
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            for info in archive.infolist():
                path = pathlib.PurePosixPath(info.filename)
                if path.is_absolute() or ".." in path.parts or "\\" in info.filename:
                    raise InstallError("ZIP contains an unsafe path")
                if stat.S_ISLNK(info.external_attr >> 16):
                    raise InstallError("ZIP contains a symbolic link")
                if info.file_size > MAX_ARCHIVE:
                    raise InstallError("ZIP member exceeds the 512 MiB limit")
    except (zipfile.BadZipFile, OSError) as exc:
        raise InstallError("download is not a valid ZIP archive") from exc


def _atomic_write(path, data, mode):
    fd, temporary = tempfile.mkstemp(prefix=".install-", dir=str(path.parent))
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--lock", default=str(pathlib.Path(__file__).resolve().parents[1] / "tools.lock.json"))
    parser.add_argument("--bin-dir", required=True)
    parser.add_argument("--cache-dir", required=True)
    parser.add_argument("--output", help="write config fragment to this file; stdout otherwise")
    args = parser.parse_args(argv)
    try:
        result = install(args.lock, platform.system(), platform.machine(), args.bin_dir, args.cache_dir)
    except InstallError as exc:
        print("install-tools: " + str(exc), file=sys.stderr)
        return 1
    encoded = json.dumps(result, indent=2) + "\n"
    if args.output:
        pathlib.Path(args.output).write_text(encoded, encoding="utf-8")
        os.chmod(args.output, 0o600)
    else:
        sys.stdout.write(encoded)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
