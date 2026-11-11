import hashlib
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
import zipfile

SCRIPT = pathlib.Path(__file__).with_name("install-tools.py")
SPEC = importlib.util.spec_from_file_location("install_tools", SCRIPT)
installer = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(installer)


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.lock = {
            "platforms": ["linux_amd64"],
            "terraform": {"version": "1.16.7", "source": "https://example.invalid/tf/", "archive": "tf-{platform}.zip", "sha256": {"linux_amd64": ""}, "binary": "terraform"},
            "terraformer": {"version": "0.6.0", "commit": "a" * 40, "source": "https://example.invalid/ter/", "archive": "ter-{platform}", "sha256": {"linux_amd64": ""}, "binary": "pingcli-terraformer"},
            "pingcli": {"version": "0.8.4", "commit": "b" * 40, "source": "https://example.invalid/ping/", "archive": "ping-{platform}", "sha256": {"linux_amd64": ""}, "binary": "pingcli"},
            "provider": {"version": "1.24.0", "commit": "c" * 40, "source": "https://example.invalid/provider/", "archive": "provider-{platform}.zip", "sha256": {"linux_amd64": ""}, "binary": "terraform-provider-pingone_v1.24.0"},
        }
        self.lock_path = self.root / "lock.json"
        self.lock_path.write_text(json.dumps(self.lock))

    def tearDown(self):
        self.temp.cleanup()

    def test_digest_checked_before_zip_is_extracted_and_public_config_returned(self):
        terraform = self.zip_bytes("terraform", b"terraform executable")
        direct = {"ter-linux_amd64": b"terraformer executable", "ping-linux_amd64": b"pingcli executable"}
        provider = self.zip_bytes("terraform-provider-pingone_v1.24.0", b"provider executable")
        for name, payload in (("terraform", terraform), ("terraformer", direct["ter-linux_amd64"]), ("pingcli", direct["ping-linux_amd64"]), ("provider", provider)):
            self.lock[name]["sha256"]["linux_amd64"] = hashlib.sha256(payload).hexdigest()
        self.lock_path.write_text(json.dumps(self.lock))
        installer.fetch_archive = lambda url: next(payload for suffix, payload in [("tf-linux_amd64.zip", terraform), *direct.items(), ("provider-linux_amd64.zip", provider)] if url.endswith(suffix))
        result = installer.install(self.lock_path, "linux", "x86_64", self.root / "bin", self.root / "cache")
        self.assertEqual((self.root / "bin/terraform").read_bytes(), b"terraform executable")
        self.assertEqual(result["tools"]["terraformVersion"], "1.16.7")
        self.assertEqual(result["tools"]["terraformSha256"], hashlib.sha256(b"terraform executable").hexdigest())
        self.assertIn("providerZip", result["tools"])

    def test_bad_digest_fails_without_extracting(self):
        archive = self.zip_bytes("terraform", b"x")
        installer.fetch_archive = lambda url: archive
        with self.assertRaises(installer.InstallError):
            installer.install(self.lock_path, "linux", "x86_64", self.root / "bin", self.root / "cache")
        self.assertFalse((self.root / "bin/terraform").exists())

    def test_zip_traversal_fails(self):
        archive = self.zip_bytes("../../escaped", b"bad")
        self.lock["terraform"]["sha256"]["linux_amd64"] = hashlib.sha256(archive).hexdigest()
        self.lock_path.write_text(json.dumps(self.lock))
        installer.fetch_archive = lambda url: archive
        with self.assertRaises(installer.InstallError):
            installer.install(self.lock_path, "linux", "x86_64", self.root / "bin", self.root / "cache")
        self.assertFalse((self.root.parent / "escaped").exists())

    def test_unsupported_platform_is_rejected(self):
        with self.assertRaises(installer.InstallError):
            installer.install(self.lock_path, "windows", "amd64", self.root / "bin", self.root / "cache")

    @staticmethod
    def zip_bytes(name, payload):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            archive.writestr(name, payload)
        return stream.getvalue()


if __name__ == "__main__":
    unittest.main()
