#!/usr/bin/env python3
"""Local packaging checks; these never install the Windows companion."""

import base64
import importlib.util
import json
from pathlib import Path
import subprocess
import struct
import tempfile
import unittest
import zipfile


SPEC = importlib.util.spec_from_file_location("windows_kit", Path(__file__).with_name("build-windows-kit.py"))
KIT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(KIT)


class WindowsKitTests(unittest.TestCase):
    def manifests(self):
        chrome = {
            "manifest_version": 3, "version": "3.5.2.1",
            "key": base64.b64encode(b"public-test-key").decode(),
            "permissions": ["proxy", "nativeMessaging"],
        }
        firefox = {
            "manifest_version": 3, "version": "3.5.2.1",
            "permissions": ["proxy", "nativeMessaging"],
            "browser_specific_settings": {"gecko": {"id": KIT.FIREFOX_ID}},
        }
        return chrome, firefox

    def test_manifest_registration_must_match_both_browsers(self):
        chrome, firefox = self.manifests()
        KIT.validate_manifests(chrome, firefox, KIT.chrome_id(chrome))
        with self.assertRaisesRegex(ValueError, "Chrome"):
            KIT.validate_manifests(chrome, firefox, "a" * 32)
        firefox["browser_specific_settings"]["gecko"]["id"] = "wrong@example.invalid"
        with self.assertRaisesRegex(ValueError, "Firefox"):
            KIT.validate_manifests(chrome, firefox, KIT.chrome_id(chrome))

    def test_native_messaging_permission_is_required(self):
        chrome, firefox = self.manifests()
        firefox["permissions"].remove("nativeMessaging")
        with self.assertRaisesRegex(ValueError, "nativeMessaging"):
            KIT.validate_manifests(chrome, firefox, KIT.chrome_id(chrome))

    def test_native_binary_must_be_windows_x64(self):
        binary = bytearray(160)
        binary[:2] = b"MZ"
        struct.pack_into("<I", binary, 0x3C, 96)
        binary[96:100] = b"PE\x00\x00"
        struct.pack_into("<H", binary, 100, 0x8664)
        KIT.validate_windows_x64_binary(binary)
        struct.pack_into("<H", binary, 100, 0xAA64)
        with self.assertRaisesRegex(ValueError, "x64"):
            KIT.validate_windows_x64_binary(binary)
        with self.assertRaisesRegex(ValueError, "PE"):
            KIT.validate_windows_x64_binary(b"MZnot-a-real-executable")

    def test_reproducible_zip_order_timestamps_and_contents(self):
        with tempfile.TemporaryDirectory() as work:
            first, second = Path(work) / "one.zip", Path(work) / "two.zip"
            KIT.write_zip(first, {"z": b"last", "a": b"first"}, 0)
            KIT.write_zip(second, {"a": b"first", "z": b"last"}, 0)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with zipfile.ZipFile(first) as archive:
                self.assertEqual(archive.namelist(), ["a", "z"])
                self.assertEqual(archive.read("a"), b"first")
                self.assertEqual(archive.getinfo("a").date_time, (1980, 1, 1, 0, 0, 0))

    def test_browser_packages_have_only_their_own_manifest(self):
        chrome, firefox = self.manifests()
        with tempfile.TemporaryDirectory() as work:
            work = Path(work)
            build, stage = work / "build", work / "stage"
            build.mkdir()
            stage.mkdir()
            (build / "manifest.json").write_text('{"stale": true}')
            (build / "manifest-firefox.json").write_text('{"stale": true}')
            (build / "options.html").write_text("<html>settings</html>")
            KIT.assemble_extension(build, chrome, firefox, 1700000000, stage)
            self.assertEqual(json.loads((stage / "chrome" / "manifest.json").read_text()), chrome)
            self.assertFalse((stage / "chrome" / "manifest-firefox.json").exists())
            with zipfile.ZipFile(stage / "firefox" / "zeroomega-wg-unsigned.xpi") as archive:
                self.assertEqual(json.loads(archive.read("manifest.json")), firefox)
                self.assertIn("options.html", archive.namelist())
                self.assertNotIn("manifest-firefox.json", archive.namelist())

    def test_source_snapshot_excludes_configs_keys_and_build_artifacts(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            paths = [
                "native-helper/main.go", "COPYING", "docs/guide.md", "test.conf",
                "key.pem", "private.key", "configs/store.dpapi", ".env", ".env.local",
                "omega-web/node_modules/lib.js", "omega-web/build/options.html",
                "dist/archive.zip", "native-helper/zeroomega-wg.exe",
                "distribution/setup/payload.zip",
            ]
            for name in paths:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("test fixture")
            files = KIT.source_files(root)
            self.assertEqual(set(files), {
                "zeroomega-wg-source/native-helper/main.go",
                "zeroomega-wg-source/COPYING",
                "zeroomega-wg-source/docs/guide.md",
            })

    def test_local_modules_are_links_to_live_sources(self):
        with tempfile.TemporaryDirectory() as work:
            root = Path(work)
            (root / "omega-pac").mkdir()
            for module in ("omega-target", "omega-web", "omega-target-chromium-extension"):
                directory = root / module
                (directory / "node_modules" / "omega-pac").mkdir(parents=True)
                (directory / "package.json").write_text(json.dumps({"dependencies": {"omega-pac": "../omega-pac"}}))
            KIT.local_module_links(root)
            for module in ("omega-target", "omega-web", "omega-target-chromium-extension"):
                link = root / module / "node_modules" / "omega-pac"
                self.assertTrue(link.is_symlink())
                self.assertEqual(link.resolve(), root / "omega-pac")


if __name__ == "__main__":
    unittest.main()
