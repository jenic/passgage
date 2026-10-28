"""Exercise maintainer publishing without network access or Git metadata writes."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent
MAKEFILE = SCRIPTS.parent.parent / "Makefile"
NOTES = "# Changelog\n\n## 1.2.3\n- Keep these notes verbatim.\n"
FAKE_TOOL = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
p = pathlib.Path
args = sys.argv[1:]
root = p(os.environ["FIXTURE"])
tool = p(sys.argv[0]).name
with (root / "calls").open("a") as log:
    log.write(json.dumps([tool, *args]) + "\n")
def fail(name):
    if os.getenv(name): sys.exit(1)
if tool == "git":
    if args == ["rev-parse", "--is-inside-work-tree"]:
        fail("NO_GIT")
        print("true")
    elif args == ["status", "--porcelain"]:
        fail("FAIL_STATUS")
        if (os.getenv("DIRTY") or
            (os.getenv("DIRTY_AFTER_TEST") and (root / "tested").exists()) or
            (os.getenv("DIRTY_AFTER_CLIFF") and (root / "cliff").exists())):
            print(" M README.md")
    elif args == ["remote", "get-url", "origin"]:
        fail("NO_ORIGIN")
        print("git@example.com:jenic/passgage.git")
    elif args[:2] == ["tag", "--list"]:
        fail("FAIL_TAG_LIST")
        if os.getenv("LOCAL_TAG"): print(args[2])
    elif args[:4] == ["ls-remote", "--exit-code", "--tags", "origin"]:
        if os.getenv("FAIL_REMOTE"): sys.exit(128)
        sys.exit(0 if os.getenv("REMOTE_TAG") else 2)
    elif args == ["rev-parse", "--verify", "HEAD^{commit}"]:
        print(("b" if os.getenv("HEAD_MOVED") and (root / "cliff").exists() else "a") * 40)
    elif args[:3] == ["tag", "-s", "--cleanup=verbatim"]:
        assert args[3] == "-F" and args[5] == os.environ["RELEASE_TAG"]
        assert args[6] == "a" * 40
        (root / "tag-notes").write_bytes(p(args[4]).read_bytes())
        fail("FAIL_SIGN")
    elif args[:2] == ["push", "origin"]:
        tag = os.environ["RELEASE_TAG"]
        assert args[2:] == [f"refs/tags/{tag}:refs/tags/{tag}"]
        fail("FAIL_PUSH")
    else: raise AssertionError(args)
elif tool == "git-cliff":
    assert args == ["--unreleased", "--tag", os.environ["RELEASE_TAG"]]
    (root / "cliff").touch()
    if not os.getenv("EMPTY_CLIFF"): print(os.environ["NOTES"], end="")
    fail("FAIL_CLIFF")
elif tool == "go":
    assert args in [["mod", "verify"], ["vet", "-mod=vendor", "./..."],
                    ["test", "-mod=vendor", "./..."]]
    assert os.environ["GOCACHE"] == str(p.cwd() / ".gocache")
    assert os.environ["GOMODCACHE"] == str(p.cwd() / ".gomodcache")
    if args[0] == "test":
        (root / "tested").touch()
        fail("FAIL_TEST")
elif tool == "gofmt":
    assert args == ["-w", "cmd", "internal", "store", "agecrypto", "tests"]
else: raise AssertionError(tool)
'''


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.worktree = self.root / "worktree"
        scripts = self.worktree / ".github/scripts"
        scripts.mkdir(parents=True)
        shutil.copyfile(MAKEFILE, self.worktree / "Makefile")
        for name in ["publish.sh", "release.sh"]:
            shutil.copyfile(SCRIPTS / name, scripts / name)
        # The recursive make uses the real test-release recipe, with an isolated test.
        (scripts / "test_check.py").write_text(
            "import os, pathlib, unittest\n"
            "class Check(unittest.TestCase):\n"
            "    def test_release(self):\n"
            "        root = pathlib.Path(os.environ['FIXTURE'])\n"
            "        (root / 'release-tested').touch()\n"
            "        self.assertFalse(os.getenv('FAIL_RELEASE_TEST'))\n")
        tools = self.root / "tools"
        tools.mkdir()
        for name in ["git", "git-cliff", "go", "gofmt"]:
            path = tools / name
            path.write_text(FAKE_TOOL)
            path.chmod(0o755)
        self.scratch = self.root / "tmp"
        self.scratch.mkdir()
        self.env = dict(os.environ, PATH=f"{tools}:{os.environ['PATH']}",
                        FIXTURE=str(self.root), NOTES=NOTES, PUBLISH="0", TMPDIR=str(self.scratch))
        # Make also exports command-line variables separately from its flags.
        # Keep release values and the jobserver out of the independent fixtures.
        for name in ["MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEOVERRIDES", "VERSION", "RELEASE_TAG"]:
            self.env.pop(name, None)

    def run_make(self, target="publish", version="v1.2.3", success=True, **env):
        args = ["make", "--no-print-directory", target]
        if version is not None:
            args.append(f"VERSION={version}")
        result = subprocess.run(args, cwd=self.worktree, env=dict(self.env, **env),
                                text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        self.assertEqual(list(self.scratch.iterdir()), [])
        return result

    def calls(self):
        path = self.root / "calls"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def writes(self):
        return [c for c in self.calls() if c[:3] in (["git", "tag", "-s"], ["git", "push", "origin"])]

    def test_check_accepts_release_versions_without_tests_or_writes(self):
        for tag in ["v0.0.0", "v1.2.3-rc.1", "v2.0.0", "v1.2.3+build-with-hyphen"]:
            with self.subTest(tag=tag):
                self.run_make("publish-check", version=tag)
        self.assertFalse((self.root / "tested").exists())
        self.assertFalse((self.root / "release-tested").exists())
        self.assertEqual(self.writes(), [])

    def test_invalid_versions_stop_before_tools(self):
        for tag in [None, "", "1.2.3", "v01.2.3", "v1.2.3-01", "v1.2.3+", "v1.2.3\n",
                    "v1.2.3';echo bad", "v1.2.3;touch injected", "v1.2.3`touch injected`"]:
            with self.subTest(tag=tag):
                self.run_make("publish-check", version=tag, success=False)
        self.assertEqual(self.calls(), [])
        self.assertFalse((self.worktree / "injected").exists())

    def test_failed_preconditions_stop_before_tests(self):
        for failure in ["NO_GIT", "FAIL_STATUS", "DIRTY", "NO_ORIGIN", "FAIL_TAG_LIST",
                        "LOCAL_TAG", "REMOTE_TAG", "FAIL_REMOTE"]:
            with self.subTest(failure=failure):
                self.run_make(success=False, PUBLISH="1", **{failure: "1"})
        self.assertFalse((self.root / "tested").exists())
        self.assertEqual(self.writes(), [])

    def test_git_cliff_is_required(self):
        (self.root / "tools/git-cliff").unlink()
        # Exclude host tools so an installed git-cliff cannot satisfy the check.
        isolated = self.root / "isolated"
        isolated.mkdir()
        for name in ["make", "bash", "dirname", "git"]:
            target = self.root / "tools/git" if name == "git" else Path(shutil.which(name))
            (isolated / name).symlink_to(target)
        result = self.run_make("publish-check", success=False, PATH=str(isolated))
        self.assertIn("git-cliff is required", result.stderr)

    def test_default_publish_runs_tests_but_requires_opt_in(self):
        result = self.run_make(success=False)
        self.assertIn("PUBLISH=1", result.stderr)
        self.assertTrue((self.root / "tested").exists())
        self.assertTrue((self.root / "release-tested").exists())
        self.assertEqual(self.writes(), [])

    def test_publish_signs_verbatim_notes_then_pushes_only_the_tag(self):
        self.run_make(PUBLISH="1")
        self.assertTrue((self.root / "tested").exists())
        self.assertTrue((self.root / "release-tested").exists())
        self.assertEqual((self.root / "tag-notes").read_text(), NOTES)
        writes = self.writes()
        self.assertEqual(len(writes), 2)
        self.assertEqual(writes[0][:4], ["git", "tag", "-s", "--cleanup=verbatim"])
        self.assertEqual(writes[1], ["git", "push", "origin", "refs/tags/v1.2.3:refs/tags/v1.2.3"])
        self.assertFalse(Path(writes[0][5]).exists())
        calls = self.calls()
        self.assertLess(calls.index(["go", "test", "-mod=vendor", "./..."]), calls.index(writes[0]))

    def test_test_failures_prevent_tag_creation(self):
        for failure in ["FAIL_TEST", "FAIL_RELEASE_TEST"]:
            with self.subTest(failure=failure):
                self.run_make(success=False, PUBLISH="1", **{failure: "1"})
        self.assertEqual(self.writes(), [])

    def test_changed_worktree_or_head_prevents_tag_creation(self):
        for failure in ["DIRTY_AFTER_TEST", "DIRTY_AFTER_CLIFF", "HEAD_MOVED"]:
            with self.subTest(failure=failure):
                (self.root / "cliff").unlink(missing_ok=True)
                self.run_make(success=False, PUBLISH="1", **{failure: "1"})
        self.assertEqual(self.writes(), [])

    def test_failed_or_empty_notes_prevent_tag_creation(self):
        for failure in ["FAIL_CLIFF", "EMPTY_CLIFF"]:
            with self.subTest(failure=failure):
                self.run_make(success=False, PUBLISH="1", **{failure: "1"})
        self.assertEqual(self.writes(), [])

    def test_failed_signing_prevents_push_and_cleans_up_message(self):
        self.run_make(success=False, PUBLISH="1", FAIL_SIGN="1")
        writes = self.writes()
        self.assertEqual(len(writes), 1)
        self.assertEqual(writes[0][:3], ["git", "tag", "-s"])
        self.assertFalse(Path(writes[0][5]).exists())

    def test_push_failure_is_reported(self):
        self.run_make(success=False, PUBLISH="1", FAIL_PUSH="1")
        self.assertEqual(len(self.writes()), 2)


if __name__ == "__main__":
    unittest.main()
