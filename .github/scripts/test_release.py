"""Offline release orchestration tests; Sigstore/GitHub calls are simulated."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release.sh").resolve()
ARCHIVES = [
    f"passgage_{system}_{arch}.{extension}"
    for system, extension in [("darwin", "tar.gz"), ("linux", "tar.gz"), ("windows", "zip")]
    for arch in ["amd64", "arm64"]
]

# These fakes exercise control flow and argument handling, not cryptography.
FAKE_TOOL = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, shutil, sys
p = pathlib.Path
args = sys.argv[1:]
root = p(os.environ["FIXTURE"])
tool = p(sys.argv[0]).name
with (root / "calls").open("a") as log:
    log.write(json.dumps([tool, *args]) + "\n")
def option(name):
    return args[args.index(name) + 1]
def digest(path):
    return hashlib.sha256(p(path).read_bytes()).hexdigest()
state = root / "state"
remote = root / "remote"
if tool == "cosign":
    blob = args[-1]
    bundle = p(option("--bundle"))
    identity = (os.environ["GITHUB_SERVER_URL"] + "/" + os.environ["GITHUB_REPOSITORY"]
                + "/.github/workflows/release.yml@refs/tags/" + os.environ["RELEASE_TAG"])
    issuer = "https://token.actions.githubusercontent.com"
    if args[0] == "sign-blob":
        assert "--yes" in args and option("--oidc-provider") == "github-actions"
        if os.getenv("FAIL_SIGN"): sys.exit(1)
        bundle.write_text(json.dumps([digest(blob), identity, issuer]))
    elif args[0] == "verify-blob":
        wanted = [digest(blob), option("--certificate-identity"), option("--certificate-oidc-issuer")]
        if os.getenv("FAIL_VERIFY") or json.loads(bundle.read_text()) != wanted: sys.exit(1)
    else: sys.exit(1)
elif args[0] == "api":
    if os.getenv("FAIL_API"): sys.exit(1)
    if "--paginate" in args:
        if state.exists(): print("true" if state.read_text() == "draft" else "false")
    elif "/git/ref/tags/" in args[1] or "/git/tags/" in args[1]:
        sha = os.environ["GITHUB_SHA"]
        if os.getenv("WRONG_SHA") or (os.getenv("MOVE_TAG_AFTER_DOWNLOAD") and (root / "downloaded").exists()):
            sha = "b" * 40
        if os.getenv("ANNOTATED") and "/git/ref/" in args[1]: print("tag\t" + "c" * 40)
        else: print("commit\t" + sha)
    else: sys.exit(1)
elif args[:2] == ["release", "create"]:
    assert "--verify-tag" in args and "--draft" in args and "--generate-notes" in args
    state.write_text("draft")
elif args[:2] == ["release", "upload"]:
    assert state.read_text() == "draft"
    remote.mkdir(exist_ok=True)
    for asset in args[3:]:
        if asset.startswith("--"): continue
        shutil.copyfile(asset, remote / asset)
        if os.getenv("FAIL_UPLOAD"): sys.exit(1)
elif args[:2] == ["release", "download"]:
    target = p(option("--dir"))
    shutil.copytree(remote, target, dirs_exist_ok=True)
    if os.getenv("TAMPER_DOWNLOAD"): (target / "SHA256SUMS.sigstore.json").write_text("[]")
    (root / "downloaded").touch()
elif args[:2] == ["release", "edit"]:
    assert "--draft=false" in args and state.read_text() == "draft"
    assert len(list(remote.iterdir())) == 14
    state.write_text("published")
else: sys.exit(1)
'''


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "go.mod").write_text("module github.com/jenic/passgage\n")
        self.dist = self.root / "dist"
        self.dist.mkdir()
        for name in ARCHIVES:
            (self.dist / name).write_bytes(name.encode())
        self.write_checksums()
        tools = self.root / "tools"
        tools.mkdir()
        for name in ["gh", "cosign"]:
            path = tools / name
            path.write_text(FAKE_TOOL)
            path.chmod(0o755)
        self.env = dict(os.environ, PATH=f"{tools}:{os.environ['PATH']}", FIXTURE=str(self.root),
                        RELEASE_TAG="v1.2.3", GITHUB_SHA="a" * 40,
                        GITHUB_REPOSITORY="jenic/passgage", GITHUB_SERVER_URL="https://github.com")

    def write_checksums(self):
        (self.dist / "SHA256SUMS").write_text("".join(
            f"{hashlib.sha256((self.dist / name).read_bytes()).hexdigest()}  {name}\n"
            for name in ARCHIVES))

    def run_release(self, command, success=True, **env):
        result = subprocess.run(["bash", str(SCRIPT), command, str(self.dist)],
                                cwd=self.root, env=dict(self.env, **env), text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return result

    def calls(self):
        path = self.root / "calls"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_semver_and_prerelease_detection(self):
        for tag, prerelease in [("v0.0.0", False), ("v1.2.3-rc.1", True),
                                ("v1.2.3-0", True), ("v1.2.3", False)]:
            with self.subTest(tag=tag):
                output = self.run_release("metadata", RELEASE_TAG=tag).stdout
                self.assertIn(f"tag={tag}\n", output)
                self.assertIn(f"prerelease={str(prerelease).lower()}\n", output)
                self.assertIn("module_path=github.com/jenic/passgage\n", output)
        for tag in ["1.2.3", "v1", "v01.2.3", "v1.2.3-01", "v1.2.3-", "v1.2.3+", "v1.2.3\n",
                    "v1.2.3;echo bad", "v1.2.3+build-with-hyphen", "v1.2.3-0+build", "v1.2.3+incompatible"]:
            with self.subTest(tag=tag):
                self.run_release("metadata", success=False, RELEASE_TAG=tag)
        self.assertEqual(self.calls(), [])

    def test_module_path_and_major_version_validation(self):
        for module, tag, success in [
            ("github.com/jenic/passgage", "v0.1.0", True),
            ('"github.com/jenic/passgage"', "v1.0.0", True),
            ("github.com/jenic/passgage", "v2.0.0", False),
            ("github.com/jenic/passgage/v2", "v2.0.0", True),
            ("github.com/jenic/passgage/v2", "v2.0.0-rc.1", True),
            ("github.com/jenic/passgage/v2", "v1.0.0", False),
            ("github.com/jenic/passgage/v2", "v3.0.0", False),
            ("github.com/jenic/passgage/v0", "v0.1.0", False),
            ("github.com/jenic/passgage/v1", "v1.0.0", False),
            ("github.com/jenic/passgage/v02", "v2.0.0", False),
            ("github.com/jenic/passgage/v10", "v10.0.0", True),
            ("passgage", "v1.0.0", False),
            ("", "v1.0.0", False),
            ("github.com/jenic//passgage", "v1.0.0", False),
            ("github.com/jenic/../passgage", "v1.0.0", False),
            ("github.com/jenic/passgage.", "v1.0.0", False),
            ("github.com/jenic/passgage extra", "v1.0.0", False),
        ]:
            with self.subTest(module=module, tag=tag):
                (self.root / "go.mod").write_text(f"module {module}\n")
                self.run_release("metadata", RELEASE_TAG=tag, success=success)
        (self.root / "go.mod").unlink()
        self.run_release("metadata", success=False)
        self.assertEqual(self.calls(), [])

    def test_asset_set_and_checksums(self):
        self.run_release("check-assets")
        for name in [ARCHIVES[0], "SHA256SUMS"]:
            path = self.dist / name
            original = path.read_bytes()
            path.write_bytes(b"tampered")
            self.run_release("check-assets", success=False)
            path.write_bytes(original)
        extra = self.dist / "unexpected"
        extra.touch()
        self.run_release("check-assets", success=False)
        extra.unlink()
        archive = self.dist / ARCHIVES[0]
        archive.unlink()
        self.run_release("check-assets", success=False)
        archive.symlink_to(self.dist / ARCHIVES[1])
        self.run_release("check-assets", success=False)

    def test_signs_every_file_and_checks_annotated_tag(self):
        self.run_release("sign", ANNOTATED="1")
        self.assertEqual(len(list(self.dist.iterdir())), 14)
        self.assertEqual(sum(c[:2] == ["cosign", "sign-blob"] for c in self.calls()), 7)
        self.assertEqual(sum(c[:2] == ["cosign", "verify-blob"] for c in self.calls()), 7)

    def test_signing_stops_on_tag_or_sigstore_failure(self):
        for failure in ["WRONG_SHA", "FAIL_API", "FAIL_SIGN", "FAIL_VERIFY"]:
            with self.subTest(failure=failure):
                for bundle in self.dist.glob("*.sigstore.json"):
                    bundle.unlink()
                self.run_release("sign", success=False, **{failure: "1"})
        self.assertFalse((self.root / "state").exists())

    def test_publish_stable_and_refuse_published_release(self):
        self.run_release("sign")
        self.run_release("publish")
        self.assertEqual((self.root / "state").read_text(), "published")
        self.assertEqual(len(list((self.root / "remote").iterdir())), 14)
        edit = next(c for c in self.calls() if c[:3] == ["gh", "release", "edit"])
        self.assertIn("--prerelease=false", edit)
        calls_before = len(self.calls())
        self.run_release("publish", success=False)
        self.assertFalse(any(c[:3] == ["gh", "release", "upload"] for c in self.calls()[calls_before:]))

    def test_prerelease_upload_failure_can_resume(self):
        self.env["RELEASE_TAG"] = "v1.2.3-rc.1"
        self.run_release("sign")
        self.run_release("publish", success=False, FAIL_UPLOAD="1")
        self.assertEqual((self.root / "state").read_text(), "draft")
        self.run_release("publish")
        self.assertEqual((self.root / "state").read_text(), "published")
        creates = [c for c in self.calls() if c[:3] == ["gh", "release", "create"]]
        self.assertEqual(len(creates), 1)
        self.assertIn("--latest=false", creates[0])
        edit = next(c for c in self.calls() if c[:3] == ["gh", "release", "edit"])
        self.assertIn("--prerelease=true", edit)
        self.assertIn("--latest=false", edit)

    def test_wrong_signer_or_issuer_prevents_publication(self):
        self.run_release("sign")
        bundle = self.dist / "SHA256SUMS.sigstore.json"
        original = json.loads(bundle.read_text())
        for field in [1, 2]:
            data = original.copy()
            data[field] = "https://untrusted.example"
            bundle.write_text(json.dumps(data))
            self.run_release("publish", success=False)
        self.assertFalse((self.root / "state").exists())

    def test_changed_archive_with_recomputed_checksums_is_rejected(self):
        self.run_release("sign")
        (self.dist / ARCHIVES[0]).write_bytes(b"changed after signing")
        self.write_checksums()
        self.run_release("publish", success=False)
        self.assertFalse((self.root / "state").exists())

    def test_download_verification_and_tag_movement_prevent_publication(self):
        self.run_release("sign")
        for failure in ["TAMPER_DOWNLOAD", "MOVE_TAG_AFTER_DOWNLOAD"]:
            with self.subTest(failure=failure):
                self.run_release("publish", success=False, **{failure: "1"})
                self.assertEqual((self.root / "state").read_text(), "draft")
        self.assertFalse(any(c[:3] == ["gh", "release", "edit"] for c in self.calls()))


if __name__ == "__main__":
    unittest.main()
