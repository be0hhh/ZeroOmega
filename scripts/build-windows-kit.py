#!/usr/bin/env python3
"""Build a self-contained Windows x64 kit without global npm links or installation."""

import argparse
import base64
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import sys
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
HOST_NAME = "io.github.zeroomega.wg"
FIREFOX_ID = "zeroomega-wg@local.invalid"
EXCLUDED_PARTS = {
    ".git", "node_modules", "bower_components", "build", "dist", "tmp",
    ".cache", "__pycache__", ".pytest_cache",
}
EXCLUDED_SUFFIXES = {".exe", ".zip", ".xpi", ".pem", ".key", ".dpapi", ".conf", ".pyc"}


def run(command, cwd=ROOT, env=None, capture=False):
    return subprocess.run(
        [str(part) for part in command], cwd=cwd, env=env, check=True,
        text=True, stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )


def chrome_id(manifest):
    """A pinned public manifest.key keeps unpacked Chrome IDs stable across PCs."""
    key = manifest.get("key")
    if not isinstance(key, str):
        raise ValueError("Chrome manifest has no pinned public key")
    try:
        public_key = base64.b64decode(key, validate=True)
    except (ValueError, base64.binascii.Error) as error:
        raise ValueError("Chrome manifest has an invalid public key") from error
    if not public_key:
        raise ValueError("Chrome manifest has an empty public key")
    digest = hashlib.sha256(public_key).hexdigest()[:32]
    return "".join(chr(ord("a") + int(nibble, 16)) for nibble in digest)


def validate_manifests(chromium, firefox, expected_id):
    if chrome_id(chromium) != expected_id:
        raise ValueError("Chrome manifest.key does not match distribution/chrome-extension-id.txt")
    if firefox.get("browser_specific_settings", {}).get("gecko", {}).get("id") != FIREFOX_ID:
        raise ValueError("Firefox ID does not match the native host installer")
    for manifest in (chromium, firefox):
        if "nativeMessaging" not in manifest.get("permissions", []):
            raise ValueError("Both manifests need nativeMessaging permission")
    if chromium.get("version") != firefox.get("version"):
        raise ValueError("Browser manifest versions differ")


def validate_windows_x64_binary(content):
    if len(content) < 64 or content[:2] != b"MZ":
        raise ValueError("Native helper is not a Windows PE executable")
    pe_offset = struct.unpack_from("<I", content, 0x3C)[0]
    if pe_offset + 26 > len(content) or content[pe_offset:pe_offset + 4] != b"PE\x00\x00":
        raise ValueError("Native helper has an invalid Windows PE header")
    machine = struct.unpack_from("<H", content, pe_offset + 4)[0]
    if machine != 0x8664:
        raise ValueError("Native helper must target Windows x64 (AMD64)")


def local_module_links(root):
    """npm 9 copies file dependencies; rebuild with live repo-local sources instead."""
    for module in ("omega-target", "omega-web", "omega-target-chromium-extension"):
        package_dir = root / module
        package = json.loads((package_dir / "package.json").read_text())
        for name, location in package.get("dependencies", {}).items():
            if not location.startswith("../omega-"):
                continue
            target = (package_dir / location).resolve()
            link = package_dir / "node_modules" / name
            if not target.is_dir() or not link.parent.is_dir():
                raise ValueError("Install project dependencies first: use --install-deps")
            if link.is_symlink():
                link.unlink()
            elif link.exists():
                shutil.rmtree(link)
            link.symlink_to(os.path.relpath(target, link.parent), target_is_directory=True)


def archive_timestamp(epoch):
    # ZIP has a 1980 lower bound and two-second timestamp precision.
    value = datetime.datetime.fromtimestamp(max(315532800, epoch), datetime.timezone.utc)
    return (value.year, value.month, value.day, value.hour, value.minute, value.second // 2 * 2)


def write_zip(destination, files, epoch):
    destination.parent.mkdir(parents=True, exist_ok=True)
    stamp = archive_timestamp(epoch)
    with zipfile.ZipFile(destination, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for name, content in sorted(files.items()):
            entry = zipfile.ZipInfo(name, stamp)
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.create_system = 3
            entry.external_attr = 0o100644 << 16
            archive.writestr(entry, content)


def tree_files(directory, prefix=""):
    return {
        prefix + path.relative_to(directory).as_posix(): path.read_bytes()
        for path in directory.rglob("*") if path.is_file() and not path.is_symlink()
    }


def safe_source_path(path):
    return not (
        any(part in EXCLUDED_PARTS for part in path.parts)
        or path.suffix.lower() in EXCLUDED_SUFFIXES
        or path.name in {".env", ".DS_Store", "zeroomega-wg"}
        or path.name.startswith(".env.")
    )


def source_files(root):
    tracked = run(["git", "ls-files", "-z"], cwd=root, capture=True).stdout.split("\0")
    additions = run(["git", "ls-files", "--others", "--exclude-standard", "-z"], cwd=root, capture=True).stdout.split("\0")
    files = {}
    for name in sorted(set(tracked + additions)):
        relative = Path(name)
        path = root / relative
        if name and safe_source_path(relative) and path.is_file() and not path.is_symlink():
            files["zeroomega-wg-source/" + relative.as_posix()] = path.read_bytes()
    return files


def patch_bytes(root):
    patch = subprocess.run(["git", "diff", "--binary", "HEAD", "--"], cwd=root, check=True, stdout=subprocess.PIPE).stdout
    additions = run(["git", "ls-files", "--others", "--exclude-standard", "-z"], cwd=root, capture=True).stdout.split("\0")
    for name in sorted(filter(None, additions)):
        relative = Path(name)
        if not safe_source_path(relative) or not (root / relative).is_file():
            continue
        result = subprocess.run(["git", "diff", "--no-index", "--binary", "--", "/dev/null", name], cwd=root, stdout=subprocess.PIPE)
        if result.returncode not in (0, 1):
            raise ValueError("Could not create source patch for " + name)
        patch += result.stdout
    return patch


def module_notices(go, module_root):
    raw = run([go, "list", "-m", "-json", "all"], cwd=module_root, capture=True).stdout
    decoder = json.JSONDecoder()
    modules = []
    while raw.strip():
        item, consumed = decoder.raw_decode(raw.lstrip())
        modules.append(item)
        raw = raw.lstrip()[consumed:]
    lines = [
        "ZeroOmega WG third-party notices", "",
        "ZeroOmega is licensed under GPL-3.0-or-later. See COPYING and AUTHORS.",
        "The accompanying source ZIP includes this fork and the Go dependencies",
        "used to build the native companion, with their upstream license notices.",
        "Dependency paths and pinned module versions:", "",
    ]
    for module in sorted(modules, key=lambda entry: entry["Path"]):
        if not module.get("Main"):
            label = module["Path"] + " " + module.get("Version", "(local replacement)")
            replacement = module.get("Replace")
            if replacement:
                label += " => " + replacement["Path"] + " " + replacement.get("Version", "(local)")
            lines.append(label)
    return "\n".join(lines) + "\n"


def assemble_extension(build_dir, chromium, firefox, epoch, stage):
    files = tree_files(build_dir)
    files.pop("manifest-firefox.json", None)
    files["manifest.json"] = (json.dumps(chromium, ensure_ascii=False, indent=2) + "\n").encode()
    for name, contents in files.items():
        path = stage / "chrome" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(contents)
    files["manifest.json"] = (json.dumps(firefox, ensure_ascii=False, indent=2) + "\n").encode()
    write_zip(stage / "firefox" / "zeroomega-wg-unsigned.xpi", files, epoch)


def build_setup(go, payload, destination):
    """Compile the setup with a payload only in its temporary build directory."""
    with tempfile.TemporaryDirectory(prefix="zeroomega-setup-build-") as work:
        work = Path(work)
        for source in (ROOT / "distribution" / "setup").iterdir():
            if source.suffix == ".go" or source.name == "go.mod":
                shutil.copy2(source, work / source.name)
        shutil.copy2(payload, work / "payload.zip")
        setup_env = os.environ.copy()
        setup_env.update({"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "0", "GOWORK": "off"})
        destination.parent.mkdir(parents=True, exist_ok=True)
        run([go, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -H=windowsgui", "-o", destination, "."], cwd=work, env=setup_env)
        validate_windows_x64_binary(destination.read_bytes())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--install-deps", action="store_true", help="Install repo-local npm/Bower dependencies first")
    parser.add_argument("--skip-extension-build", action="store_true", help="Package an already built extension")
    parser.add_argument("--go", default="go", help="Go executable (Go 1.25 or newer)")
    parser.add_argument("--npm", default="npm", help="npm executable")
    parser.add_argument("--helper-binary", type=Path, help="Use an existing Windows x64 binary instead of cross-compiling")
    parser.add_argument("--output", type=Path, default=ROOT / "dist" / "zeroomega-wg-windows-x64.zip")
    parser.add_argument("--setup-output", type=Path, help="Single-file installer output (default: next to the ZIP)")
    args = parser.parse_args(argv)
    build_env = os.environ.copy()
    if args.install_deps:
        # git:// is often blocked; configure rewriting only for this subprocess.
        git_count = int(build_env.get("GIT_CONFIG_COUNT", "0"))
        build_env.update({
            "GIT_CONFIG_COUNT": str(git_count + 1),
            "GIT_CONFIG_KEY_" + str(git_count): "url.https://github.com/.insteadOf",
            "GIT_CONFIG_VALUE_" + str(git_count): "git://github.com/",
        })
        run([args.npm, "run", "deps"], cwd=ROOT / "omega-build", env=build_env)
    if not args.skip_extension_build:
        local_module_links(ROOT)
        run([args.npm, "run", "build"], cwd=ROOT / "omega-build", env=build_env)
        run([args.npm, "run", "release"], cwd=ROOT / "omega-build", env=build_env)

    target = ROOT / "omega-target-chromium-extension"
    build_dir = target / "build"
    if not (build_dir / "options.html").is_file():
        raise ValueError("No built extension. Run without --skip-extension-build after installing dependencies")
    chromium = json.loads((target / "overlay" / "manifest.json").read_text())
    firefox = json.loads((target / "overlay" / "manifest-firefox.json").read_text())
    expected_id = (ROOT / "distribution" / "chrome-extension-id.txt").read_text().strip()
    validate_manifests(chromium, firefox, expected_id)
    epoch = int(os.environ.get("SOURCE_DATE_EPOCH", run(["git", "show", "-s", "--format=%ct", "HEAD"], capture=True).stdout.strip()))
    revision = run(["git", "rev-parse", "HEAD"], capture=True).stdout.strip()
    go_version = run([args.go, "version"], capture=True).stdout.strip()

    with tempfile.TemporaryDirectory(prefix="zeroomega-windows-kit-") as work:
        work = Path(work)
        stage = work / "zeroomega-wg-windows-x64"
        stage.mkdir()
        assemble_extension(build_dir, chromium, firefox, epoch, stage)
        helper = stage / "zeroomega-wg.exe"
        if args.helper_binary:
            shutil.copy2(args.helper_binary, helper)
        else:
            native_env = os.environ.copy()
            native_env.update({"GOOS": "windows", "GOARCH": "amd64", "CGO_ENABLED": "0"})
            run([args.go, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -H=windowsgui", "-o", helper, "."], cwd=ROOT / "native-helper", env=native_env)
        validate_windows_x64_binary(helper.read_bytes())
        for name in ("install.ps1", "uninstall.ps1", "chrome-extension-id.txt", "START-HERE.html"):
            shutil.copy2(ROOT / "distribution" / name, stage / name)
        for name in ("COPYING", "AUTHORS"):
            shutil.copy2(ROOT / name, stage / name)
        go_root = Path(run([args.go, "env", "GOROOT"], capture=True).stdout.strip())
        shutil.copy2(go_root / "LICENSE", stage / "Go-LICENSE.txt")
        shutil.copy2(ROOT / "docs" / "WIREGUARD.ru.md", stage / "README.ru.md")
        shutil.copy2(ROOT / "docs" / "WIREGUARD.en.md", stage / "README.en.md")
        sources = source_files(ROOT)
        # Ship the exact native dependency sources, rather than only a source offer.
        vendor = work / "vendor"
        run([args.go, "mod", "vendor", "-o", vendor], cwd=ROOT / "native-helper")
        sources.update(tree_files(vendor, "zeroomega-wg-source/native-helper/vendor/"))
        write_zip(stage / "source" / "zeroomega-wg-source.zip", sources, epoch)
        patch = patch_bytes(ROOT)
        (stage / "source" / "zeroomega-wg.patch").write_bytes(patch)
        (stage / "THIRD_PARTY_NOTICES.txt").write_text(module_notices(args.go, ROOT / "native-helper"), encoding="utf-8")
        info = {
            "version": chromium["version"], "upstreamCommit": revision,
            "patchSha256": hashlib.sha256(patch).hexdigest(),
            "chromeExtensionId": expected_id, "firefoxExtensionId": FIREFOX_ID,
            "nativeHost": HOST_NAME, "go": go_version,
            "target": "windows/amd64", "sourceDateEpoch": epoch,
            "firefoxSigned": False,
        }
        (stage / "BUILD-INFO.json").write_text(json.dumps(info, indent=2) + "\n", encoding="utf-8")
        output = args.output.resolve()
        write_zip(output, tree_files(stage, "zeroomega-wg-windows-x64/"), epoch)
    setup_output = (args.setup_output or output.with_name("ZeroOmega-WG-Setup.exe")).resolve()
    build_setup(args.go, output, setup_output)
    print(output)
    print(setup_output)
    print("Firefox XPI is UNSIGNED. Obtain unlisted Mozilla signing before permanent installation.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print("Windows kit build failed: " + str(error), file=sys.stderr)
        sys.exit(1)
