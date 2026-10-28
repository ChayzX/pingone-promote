#!/usr/bin/env python3
"""Run an isolated, real Homebrew install/upgrade/uninstall lifecycle test.

This harness uses a temporary local tap and temporary app data. It refuses to
run if the formula or tap name it needs is already present in Homebrew.
"""
import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from release import valid_version


REPOSITORY = Path(__file__).resolve().parents[1]
FORMULA_NAME = "pingone-promote"
STARTUP = re.compile(r"Open: (http://127\.0\.0\.1:(\d+)/\?token=([^\s]+))")


def run(argv, *, cwd=None, check=True, capture=False):
    print("+", " ".join(str(part) for part in argv), flush=True)
    env = None
    if Path(str(argv[0])).name == "brew":
        # Keep Homebrew's global metadata and unrelated packages/taps untouched.
        env = {
            **os.environ,
            "HOMEBREW_NO_ANALYTICS": "1",
            "HOMEBREW_NO_AUTO_UPDATE": "1",
            "HOMEBREW_NO_INSTALL_CLEANUP": "1",
            "HOMEBREW_NO_AUTOREMOVE": "1",
        }
    return subprocess.run(
        [str(part) for part in argv],
        cwd=cwd,
        env=env,
        check=check,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
    )


def output(argv, *, cwd=None):
    return run(argv, cwd=cwd, capture=True).stdout.strip()


def git(repo, *args):
    return output(["git", "-C", repo, *args])


def validate_checkout(repo, ref):
    head_tree = git(repo, "rev-parse", "HEAD^{tree}")
    ref_tree = git(repo, "rev-parse", "--verify", f"{ref}^{{tree}}")
    if head_tree != ref_tree:
        raise RuntimeError(f"packaged ref {ref!r} does not match the current checkout tree")
    dirty = git(repo, "status", "--porcelain", "--untracked-files=all")
    if dirty:
        raise RuntimeError("working tree is dirty; commit or remove changes before packaging:\n" + dirty)
    return head_tree


def brew_formula_installed(brew):
    result = run([brew, "list", "--formula", FORMULA_NAME], check=False, capture=True)
    if result.returncode == 0:
        return True
    # Homebrew's 'list formula' uses exit status 1 when the formula is absent.
    if result.returncode == 1:
        return False
    raise RuntimeError(f"could not determine whether {FORMULA_NAME} is installed: {result.stdout.strip()}")


def installed_formula_info(brew):
    result = run([brew, "info", "--json=v2", "--installed"], check=False, capture=True)
    if result.returncode != 0:
        raise RuntimeError(f"could not determine installed formula ownership: {result.stdout.strip()}")
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError("Homebrew returned invalid JSON while checking installed formula ownership") from error
    formulae = payload.get("formulae")
    if not isinstance(formulae, list):
        raise RuntimeError("Homebrew JSON did not contain an installed formula list")
    return next(
        (formula for formula in formulae if formula.get("name") == FORMULA_NAME),
        None,
    )


def formula_owned_by_tap(brew, tap_name):
    info = installed_formula_info(brew)
    if info is None:
        return False
    return info.get("tap") == tap_name or info.get("full_name") == f"{tap_name}/{FORMULA_NAME}"


def tap_names(brew):
    return set(output([brew, "tap"]).splitlines())


def next_version(version):
    valid_version(version)
    major, minor, patch = (int(part) for part in version[1:].split("-", 1)[0].split("."))
    return f"v{major}.{minor}.{patch + 1}"


def release_source(repo, ref, version, destination):
    run([sys.executable, repo / "scripts" / "release.py", "source", "--version", version, "--ref", ref, "--repo", repo, "--output", destination])
    run([sys.executable, repo / "scripts" / "release.py", "verify", "--directory", destination])
    archive = destination / f"{FORMULA_NAME}-{version}-source.tar.gz"
    formula_path = destination / f"{FORMULA_NAME}.rb"
    run([sys.executable, repo / "scripts" / "release.py", "formula", "--version", version, "--archive", archive, "--output", formula_path, "--local-source"])
    return formula_path


def seed_external_data(data_dir):
    data_dir.mkdir(mode=0o700)
    profiles = b"[]\n"
    history = b"[{}]\n"
    marker = b"homebrew-lifecycle-data-marker\x00\xff\n"
    (data_dir / "profiles.json").write_bytes(profiles)
    (data_dir / "records.json").write_bytes(history)
    (data_dir / "lifecycle-marker.bin").write_bytes(marker)
    return {
        "profiles.json": profiles,
        "records.json": history,
        "lifecycle-marker.bin": marker,
    }


def assert_external_data(data_dir, expected):
    for name, contents in expected.items():
        actual = (data_dir / name).read_bytes()
        if actual != contents:
            raise RuntimeError(f"external app data changed: {name}")


def http_request(opener, url, *, headers=None, expected_status=200):
    request = urllib.request.Request(url, headers=headers or {})
    try:
        response = opener.open(request, timeout=5)
    except urllib.error.HTTPError as error:
        if error.code != expected_status:
            raise RuntimeError(f"GET {redact_url(url)} returned {error.code}; expected {expected_status}") from error
        return error.code, error.read()
    except urllib.error.URLError as error:
        raise RuntimeError(f"request to {redact_url(url)} failed: {error.reason}") from error
    if response.status != expected_status:
        raise RuntimeError(f"GET {redact_url(url)} returned {response.status}; expected {expected_status}")
    return response.status, response.read()


def redact_url(url):
    parts = urllib.parse.urlsplit(url)
    if not parts.query:
        return url
    return urllib.parse.urlunsplit((parts.scheme, parts.netloc, parts.path, "<redacted>", ""))


def redact_log(contents):
    return STARTUP.sub("Open: <bootstrap URL redacted>", contents)


def app_startup_probe(binary, data_dir, log_path):
    with log_path.open("w+b") as log:
        process = subprocess.Popen(
            [str(binary), "--no-browser", "--listen", "127.0.0.1:0", "--data-dir", str(data_dir)],
            stdout=log,
            stderr=subprocess.STDOUT,
        )
        startup_url = None
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if process.poll() is not None:
                break
            log.flush()
            contents = log_path.read_text(encoding="utf-8", errors="replace")
            match = STARTUP.search(contents)
            if match:
                startup_url = match.group(1)
                break
            time.sleep(0.1)
        if startup_url is None:
            if process.poll() is None:
                process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            logs = log_path.read_text(encoding="utf-8", errors="replace")
            raise RuntimeError("application did not print its loopback startup URL:\n" + redact_log(logs))

        cookie_jar = http.cookiejar.CookieJar()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(cookie_jar))
        base_url = f"http://127.0.0.1:{match.group(2)}"
        try:
            # No bootstrap cookie is allowed before using the one-time startup link.
            unauth = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            http_request(unauth, base_url + "/api/session", expected_status=401)
            http_request(opener, startup_url)
            if not any(cookie.name == "pingone_local" for cookie in cookie_jar):
                raise RuntimeError("bootstrap link did not establish the local session cookie")

            status, session_raw = http_request(opener, base_url + "/api/session")
            session = json.loads(session_raw)
            csrf = session.get("csrf")
            if status != 200 or not isinstance(csrf, str) or not csrf:
                raise RuntimeError("authenticated session probe did not return a CSRF token")
            if session.get("localTargets"):
                raise RuntimeError("app unexpectedly enabled direct promotion targets")

            # The probes are read-only. An attempted write without CSRF must be blocked.
            request = urllib.request.Request(
                base_url + "/api/profiles",
                data=b"{}",
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            try:
                opener.open(request, timeout=5)
            except urllib.error.HTTPError as error:
                if error.code != 403:
                    raise RuntimeError(f"POST without CSRF returned {error.code}; expected 403") from error
            else:
                raise RuntimeError("POST without CSRF was not rejected")

            # Cross-site requests must be rejected even with the local session cookie.
            request = urllib.request.Request(base_url + "/api/session", headers={"Sec-Fetch-Site": "cross-site"})
            try:
                opener.open(request, timeout=5)
            except urllib.error.HTTPError as error:
                if error.code != 403:
                    raise RuntimeError(f"cross-site probe returned {error.code}; expected 403") from error
            else:
                raise RuntimeError("cross-site request was not rejected")

            _, profiles = http_request(opener, base_url + "/api/profiles")
            _, history = http_request(opener, base_url + "/api/history")
            if json.loads(profiles) != []:
                raise RuntimeError("unexpected profiles appeared during read-only probes")
            if len(json.loads(history)) != 1:
                raise RuntimeError("history read probe did not return the seeded history marker")
        finally:
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
            try:
                exit_code = process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise RuntimeError("application did not stop after SIGTERM")
            if exit_code != 0:
                raise RuntimeError(f"application exited with status {exit_code} after SIGTERM")


def initialize_local_tap(tap_source, formula_path, tap_name):
    tap_source.mkdir()
    shutil.copyfile(formula_path, tap_source / f"{FORMULA_NAME}.rb")
    run(["git", "init", "--initial-branch=main"], cwd=tap_source)
    run(["git", "add", f"{FORMULA_NAME}.rb"], cwd=tap_source)
    run(["git", "-c", "user.name=Homebrew lifecycle harness", "-c", "user.email=homebrew-lifecycle@localhost", "commit", "-m", "Add local lifecycle formula"], cwd=tap_source)
    return tap_name


def commit_tap_formula(tap_source, formula_path, version):
    shutil.copyfile(formula_path, tap_source / f"{FORMULA_NAME}.rb")
    run(["git", "add", f"{FORMULA_NAME}.rb"], cwd=tap_source)
    run(["git", "-c", "user.name=Homebrew lifecycle harness", "-c", "user.email=homebrew-lifecycle@localhost", "commit", "-m", f"Upgrade formula to {version}"], cwd=tap_source)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, help="release version to package, for example v0.2.0-rc.1")
    parser.add_argument("--ref", default="HEAD", help="Git ref to package; must resolve to the current clean checkout tree")
    parser.add_argument("--repo", type=Path, default=REPOSITORY, help=argparse.SUPPRESS)
    args = parser.parse_args()

    if os.name != "posix":
        raise RuntimeError("the Homebrew lifecycle harness requires a macOS or Linux host")
    valid_version(args.version)

    repo = args.repo.resolve()
    tree = validate_checkout(repo, args.ref)
    brew = shutil.which("brew")
    if not brew:
        raise RuntimeError("Homebrew is not installed or is not on PATH")
    print("Homebrew:", output([brew, "--version"]).splitlines()[0])
    if brew_formula_installed(brew):
        raise RuntimeError(f"refusing to touch existing installed formula {FORMULA_NAME}")

    identifier = uuid.uuid4().hex[:12]
    owner = f"codexlifecycle{identifier}"
    repository = f"homebrew-pingone-promote-{identifier}"
    tap_name = f"{owner}/pingone-promote-{identifier}"
    if tap_name in tap_names(brew):
        raise RuntimeError(f"refusing to touch existing tap {tap_name}")

    brew_repository = Path(output([brew, "--repository"]))
    tap_checkout = brew_repository / "Library" / "Taps" / owner / repository
    if tap_checkout.exists():
        raise RuntimeError(f"refusing to touch existing Homebrew tap path {tap_checkout}")

    upgraded_version = next_version(args.version)
    created_tap = False
    installed_formula = False
    install_attempted = False
    cleanup_errors = []
    safe_to_untap = True
    try:
        with tempfile.TemporaryDirectory(prefix="pingone-homebrew-lifecycle-") as temporary:
            root = Path(temporary)
            first_output = root / "release-first"
            second_output = root / "release-upgrade"
            tap_source = root / "tap-source"
            data_dir = root / "app-data"
            expected_data = seed_external_data(data_dir)

            first_formula = release_source(repo, args.ref, args.version, first_output)
            second_formula = release_source(repo, args.ref, upgraded_version, second_output)
            initialize_local_tap(tap_source, first_formula, tap_name)

            # The random, preflight-checked name is ours from this point on;
            # the finalizer checks whether Homebrew registered it before untapping.
            created_tap = True
            run([brew, "tap", "--custom-remote", tap_name, tap_source])
            if tap_name not in tap_names(brew):
                raise RuntimeError("Homebrew did not register the temporary local tap")
            install_attempted = True
            run([brew, "install", "--formula", f"{tap_name}/{FORMULA_NAME}"])
            installed_formula = True
            if not formula_owned_by_tap(brew, tap_name):
                raise RuntimeError(f"Homebrew did not report {FORMULA_NAME} as installed from temporary tap {tap_name}")

            installed = output([brew, "list", "--formula", "--versions", FORMULA_NAME])
            if args.version[1:] not in installed:
                raise RuntimeError(f"installed formula does not report requested version {args.version}: {installed}")
            run([brew, "test", f"{tap_name}/{FORMULA_NAME}"])
            prefix = Path(output([brew, "--prefix", FORMULA_NAME]))
            main_binary = prefix / "bin" / FORMULA_NAME
            runner_binary = prefix / "bin" / "pingone-runner"
            for binary, expected in ((main_binary, f"pingone-promote {args.version}\n"), (runner_binary, f"pingone-runner {args.version}\n")):
                actual = run([binary, "--version"], capture=True).stdout
                if actual != expected:
                    raise RuntimeError(f"unexpected --version output from {binary}: {actual!r}")

            app_startup_probe(main_binary, data_dir, root / "app-first.log")
            assert_external_data(data_dir, expected_data)

            commit_tap_formula(tap_source, second_formula, upgraded_version)
            run(["git", "-C", tap_checkout, "pull", "--ff-only"])
            run([brew, "upgrade", "--formula", f"{tap_name}/{FORMULA_NAME}"])
            upgraded = output([brew, "list", "--formula", "--versions", FORMULA_NAME])
            if upgraded_version[1:] not in upgraded:
                raise RuntimeError(f"formula did not upgrade to {upgraded_version}: {upgraded}")
            run([brew, "test", f"{tap_name}/{FORMULA_NAME}"])

            upgraded_prefix = Path(output([brew, "--prefix", FORMULA_NAME]))
            upgraded_binary = upgraded_prefix / "bin" / FORMULA_NAME
            actual = run([upgraded_binary, "--version"], capture=True).stdout
            if actual != f"pingone-promote {upgraded_version}\n":
                raise RuntimeError(f"upgraded binary reports an unexpected version: {actual!r}")
            app_startup_probe(upgraded_binary, data_dir, root / "app-upgraded.log")
            assert_external_data(data_dir, expected_data)

            if not formula_owned_by_tap(brew, tap_name):
                raise RuntimeError(f"refusing to uninstall {FORMULA_NAME}; installed formula is not owned by temporary tap {tap_name}")
            run([brew, "uninstall", "--force", "--formula", f"{tap_name}/{FORMULA_NAME}"])
            if brew_formula_installed(brew):
                raise RuntimeError(f"{FORMULA_NAME} remains installed after uninstall")
            installed_formula = False
            install_attempted = False
            assert_external_data(data_dir, expected_data)
            print(f"Homebrew lifecycle passed: {args.version} -> {upgraded_version}; external app data/history remained byte-identical")
    finally:
        if install_attempted or installed_formula:
            try:
                info = installed_formula_info(brew)
                if info is None:
                    installed_formula = False
                    install_attempted = False
                elif formula_owned_by_tap(brew, tap_name):
                    run([brew, "uninstall", "--force", "--formula", f"{tap_name}/{FORMULA_NAME}"])
                    if formula_owned_by_tap(brew, tap_name):
                        raise RuntimeError(f"{FORMULA_NAME} remains installed from temporary tap {tap_name}")
                    installed_formula = False
                    install_attempted = False
                else:
                    cleanup_errors.append(f"leaving installed {FORMULA_NAME} untouched because it is not owned by temporary tap {tap_name}")
            except Exception as error:  # preserve the original failure and report cleanup trouble
                safe_to_untap = False
                cleanup_errors.append(str(error))
        if created_tap:
            try:
                if not safe_to_untap:
                    cleanup_errors.append(f"leaving {tap_name} in place because formula ownership could not be verified")
                elif tap_name in tap_names(brew):
                    run([brew, "untap", tap_name])
                    if tap_name in tap_names(brew):
                        raise RuntimeError(f"temporary tap {tap_name} remains registered after untap")
            except Exception as error:
                cleanup_errors.append(str(error))
        if cleanup_errors:
            message = "Homebrew lifecycle cleanup errors: " + "; ".join(cleanup_errors)
            print(message, file=sys.stderr)
            if sys.exc_info()[0] is None:
                raise RuntimeError(message)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError) as error:
        print(f"Homebrew lifecycle failed: {error}", file=sys.stderr)
        sys.exit(1)
