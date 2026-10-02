#!/usr/bin/env python3
"""Offline regression tests for the repository's maintenance workflows.

Run: python3 scripts/test-maintenance-workflows.py
The exact Bash run blocks are tested in disposable Git repositories. Only network
services and the server build are stubbed; these tests do not replace Go tests.
"""

import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"
EXPRESSION = re.compile(r"\$\{\{\s*(.*?)\s*\}\}")
VERSION = "0.159.2"
NEW_VERSION = "0.160.0"
UA = f"codex-tui/{VERSION} (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; {VERSION})"


def workflow_step(filename, name, values=None):
    """Extract our fixed-indent scalar env/run fields, not a general YAML parser.

    Fail closed if the workflow changes to an unsupported representation. Keeping
    this small avoids installing a YAML dependency merely to test shell scripts.
    """
    values = values or {}
    text = (WORKFLOWS / filename).read_text(encoding="utf-8")
    steps = text.split("      - name: ")[1:]
    matches = [step for step in steps if step.splitlines()[0] == name]
    if len(matches) != 1:
        raise AssertionError(f"expected one step {name!r} in {filename}")
    step = matches[0]

    def render(text):
        return EXPRESSION.sub(lambda match: values[match[1]], text)

    env = {}
    env_match = re.search(r"^        env:\n((?:          .+\n)+)", step, re.M)
    if env_match:
        for line in env_match[1].splitlines():
            key, value = line.strip().split(": ", 1)
            env[key] = render(value)
    match = re.search(r"^        run: (.+)\n?", step, re.M)
    if not match:
        raise AssertionError(f"missing run block in {name!r}")
    if match[1] == "|":
        lines = []
        for line in step[match.end():].splitlines():
            if line and not line.startswith("          "):
                break
            lines.append(line[10:] if line else "")
        script = "\n".join(lines) + "\n"
    else:
        script = match[1] + "\n"
    return render(script), env


class MaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="clirelay-maintenance-")
        self.addCleanup(self.tmp.cleanup)
        self.base = Path(self.tmp.name)
        self.repo = self.base / "repo"
        self.repo.mkdir()
        self.bin = self.base / "bin"
        self.bin.mkdir()
        self.env = dict(os.environ, PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}",
                        GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                        GIT_AUTHOR_NAME="Workflow test", GIT_AUTHOR_EMAIL="test@example.invalid",
                        GIT_COMMITTER_NAME="Workflow test", GIT_COMMITTER_EMAIL="test@example.invalid",
                        GIT_TERMINAL_PROMPT="0", GITHUB_OUTPUT=str(self.base / "outputs"),
                        BUILD_LOG=str(self.base / "build.log"),
                        GH_LOG=str(self.base / "gh.jsonl"), GH_EXISTING="")
        self.git("init", "-q", "-b", "main")
        self.write("scripts/update-codex-version.sh", (ROOT / "scripts/update-codex-version.sh").read_text())
        self.write("internal/config/config.go", "package config\n")
        self.write("internal/config/local_types.go", textwrap.dedent(f'''\
            package config
            const (
                DefaultCodexFingerprintUserAgent     = "{UA}"
                DefaultCodexFingerprintVersion       = "{VERSION}"
            )
            '''))
        self.write("internal/runtime/executor/codex_executor_request.go",
                   f'package executor\nconst userAgent = "{UA}"\n')
        self.write("internal/config/identity_fingerprint_test.go",
                   f'package config\nconst expectedVersion = "{VERSION}"\nconst expectedUA = "{UA}"\n')
        self.write("panel/package.json", '{"name":"fixture-panel"}\n')
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")
        self.stub("go", '#!/bin/sh\nprintf "%s\\n" "$*" >> "$BUILD_LOG"\nexit "${BUILD_EXIT:-0}"\n')
        self.stub("curl", '#!/bin/sh\nprintf \'{"version":"%s"}\\n\' "${NPM_VERSION:-0.160.0}"\n')
        self.stub("gh", '#!/usr/bin/env python3\nimport json, os, sys\n'
                  'with open(os.environ["GH_LOG"], "a") as f: f.write(json.dumps(sys.argv[1:]) + "\\n")\n'
                  'if sys.argv[1:3] == ["pr", "list"]: print(os.environ.get("GH_EXISTING", ""))\n')

    def write(self, path, content):
        dest = self.repo / path
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(content, encoding="utf-8")
        return dest

    def stub(self, name, content):
        dest = self.bin / name
        dest.write_text(content, encoding="utf-8")
        dest.chmod(0o755)

    def run_cmd(self, *args, check=True, cwd=None, env=None):
        result = subprocess.run(args, cwd=cwd or self.repo, env=env or self.env,
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=20)
        if check:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def git(self, *args, **kwargs):
        return self.run_cmd("git", *args, **kwargs).stdout.strip()

    def updater(self, *args, **kwargs):
        return self.run_cmd("bash", "scripts/update-codex-version.sh", *args, **kwargs)

    def step(self, filename, name, values=None, check=True):
        script, env = workflow_step(filename, name, values)
        script = script.replace("/tmp/codeproxy", "${UPSTREAM_DIR}")
        return self.run_cmd("bash", "-euo", "pipefail", "-c", script,
                            check=check, env=dict(self.env, **env))

    def source_snapshot(self):
        return {str(p.relative_to(self.repo)): p.read_bytes() for p in self.repo.rglob("*.go")}

    def test_current_version_is_read_only(self):
        before = self.source_snapshot()
        self.assertEqual(self.updater("--current").stdout.strip(), VERSION)
        self.assertEqual(self.source_snapshot(), before)
        self.assertFalse((self.base / "build.log").exists())

    def test_update_and_repeat_are_consistent(self):
        self.updater(NEW_VERSION)
        self.assertEqual(self.updater("--current").stdout.strip(), NEW_VERSION)
        for contents in self.source_snapshot().values():
            self.assertNotIn(VERSION.encode(), contents)
        before = self.source_snapshot()
        build_log = (self.base / "build.log").read_bytes()
        self.updater(NEW_VERSION)
        self.assertEqual(self.source_snapshot(), before)
        self.assertEqual((self.base / "build.log").read_bytes(), build_log)

    def test_tabs_do_not_prevent_version_replacement(self):
        path = self.repo / "internal/config/local_types.go"
        path.write_text(path.read_text().replace('Version       =', 'Version\t='))
        self.updater(NEW_VERSION)
        self.assertIn(f'"{NEW_VERSION}"', path.read_text())
        self.assertNotIn(f'"{VERSION}"', path.read_text())

    def test_invalid_target_versions_leave_sources_untouched(self):
        originals = self.source_snapshot()
        for value in ("", "bad", '1.2.3"', "1.2.3\n4.5.6", "$(touch injected)"):
            with self.subTest(value=value):
                for path, data in originals.items():
                    (self.repo / path).write_bytes(data)
                before = self.source_snapshot()
                self.assertNotEqual(self.updater(value, check=False).returncode, 0)
                self.assertEqual(self.source_snapshot(), before)
                self.assertFalse((self.base / "build.log").exists())

    def test_missing_or_duplicate_current_version_fails_before_writes(self):
        path = self.repo / "internal/config/local_types.go"
        for text in ('package config\n', f'package config\nconst (\nDefaultCodexFingerprintVersion = "{VERSION}"\nDefaultCodexFingerprintVersion = "{VERSION}"\n)\n'):
            with self.subTest(text=text):
                path.write_text(text)
                before = self.source_snapshot()
                self.assertNotEqual(self.updater(NEW_VERSION, check=False).returncode, 0)
                self.assertEqual(self.source_snapshot(), before)
                self.assertFalse((self.base / "build.log").exists())

    def test_prerelease_version_is_supported(self):
        version = "0.160.0-beta.1+build.2"
        self.updater(version)
        self.assertEqual(self.updater("--current").stdout.strip(), version)

    def test_build_failure_is_not_reported_as_success(self):
        self.env["BUILD_EXIT"] = "1"
        self.assertNotEqual(self.updater(NEW_VERSION, check=False).returncode, 0)

    def test_version_workflow_changed_and_unchanged_outputs(self):
        for target, expected in ((VERSION, "changed=false"), (NEW_VERSION, "changed=true")):
            with self.subTest(target=target):
                (self.base / "outputs").write_text("")
                self.step("update-codex-version.yml", "Run update script",
                          {"github.event.inputs.version || ''": target})
                outputs = (self.base / "outputs").read_text()
                self.assertIn(expected, outputs)
                if target == NEW_VERSION:
                    self.assertIn(f"version={NEW_VERSION}\n", outputs)

    def test_scheduled_version_update_uses_registry_result(self):
        self.step("update-codex-version.yml", "Run update script",
                  {"github.event.inputs.version || ''": ""})
        self.assertIn(f"version={NEW_VERSION}\n", (self.base / "outputs").read_text())

    def test_dispatch_value_is_data_not_shell(self):
        # Repair old-path lookup too, so this catches interpolation independently.
        shutil.copyfile(self.repo / "internal/config/local_types.go", self.repo / "internal/config/config.go")
        result = self.step("update-codex-version.yml", "Run update script",
                           {"github.event.inputs.version || ''": '$(touch injected)'}, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.repo / "injected").exists())

    def test_panel_push_targets_new_sync_commit_not_main_or_stale_branch(self):
        remote = self.base / "remote.git"
        self.run_cmd("git", "init", "-q", "--bare", str(remote))
        self.git("remote", "add", "origin", str(remote))
        self.git("push", "-q", "origin", "main")
        initial = self.git("rev-parse", "HEAD")
        upstream = self.base / "upstream"
        upstream.mkdir()
        self.run_cmd("git", "init", "-q", "-b", "main", cwd=upstream)
        (upstream / "package.json").write_text('{"name":"upstream-panel"}\n')
        self.run_cmd("git", "add", ".", cwd=upstream)
        self.run_cmd("git", "commit", "-qm", "upstream", cwd=upstream)
        self.env.update(UPSTREAM_DIR=str(upstream), PANEL_DIR="panel", BASE_BRANCH="main",
                        SYNC_BRANCH="chore/sync-codeproxy-panel", UPSTREAM_REPOSITORY="fixture/panel")
        # Copy fixtures without invoking a network service; rsync itself is not under test.
        self.stub("rsync", '#!/usr/bin/env python3\nimport shutil, sys\n'
                  'shutil.copytree(sys.argv[-2], sys.argv[-1], dirs_exist_ok=True, ignore=shutil.ignore_patterns(".git"))\n')
        for stale in (False, True):
            with self.subTest(stale=stale):
                self.git("checkout", "-q", "main")
                if stale:
                    self.git("branch", "-f", self.env["SYNC_BRANCH"], initial)
                self.step("sync-codeproxy.yml", "Sync panel directory")
                expected = self.git("rev-parse", "HEAD")
                self.assertNotEqual(expected, initial)
                self.step("sync-codeproxy.yml", "Push sync branch")
                actual = self.git("--git-dir", str(remote), "rev-parse", f'refs/heads/{self.env["SYNC_BRANCH"]}')
                self.assertEqual(actual, expected)
                self.assertEqual(self.git("--git-dir", str(remote), "rev-parse", "refs/heads/main"), initial)

    def test_upstream_commit_subjects_are_literal_pr_text(self):
        self.env.update(GITHUB_REPOSITORY="fixture/relay", GITHUB_REPOSITORY_OWNER="fixture",
                        BASE_BRANCH="main", SYNC_BRANCH="chore/sync-panel",
                        UPSTREAM_REPOSITORY="fixture/panel", UPSTREAM_BRANCH="main")
        subject = 'abc123 fix: $(touch injected) "quoted" `touch backtick-injected`'
        values = {"secrets.SYNC_PR_TOKEN != '' && secrets.SYNC_PR_TOKEN || secrets.GITHUB_TOKEN": "fixture-token",
                  "steps.sync.outputs.upstream_sha": "abc123", "steps.sync.outputs.upstream_log": subject}
        for existing in ("", "42"):
            with self.subTest(existing=existing):
                self.env["GH_EXISTING"] = existing
                self.step("sync-codeproxy.yml", "Create or update pull request", values)
                self.assertFalse((self.repo / "injected").exists())
                self.assertFalse((self.repo / "backtick-injected").exists())
                args = json.loads((self.base / "gh.jsonl").read_text().splitlines()[-1])
                self.assertEqual(args[1], "edit" if existing else "create")
                self.assertIn(subject, args[args.index("--body") + 1])


if __name__ == "__main__":
    unittest.main(verbosity=2)
