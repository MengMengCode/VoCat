import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("auto_release", Path(__file__).with_name("auto-release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def published(tag):
    return {"tag_name": tag, "draft": False, "prerelease": False, "published_at": "2026-10-24T00:00:00Z",
            "html_url": "https://example.test/releases/" + tag,
            "assets": [{"name": name, "state": "uploaded", "size": 1} for name in release.ASSETS]}


def run_info(number, status="completed", conclusion="success"):
    return {"id": number, "status": status, "conclusion": conclusion,
            "html_url": f"https://example.test/actions/runs/{number}"}


class PlanTests(unittest.TestCase):
    def plan(self, lines, head="fixed"):
        def git(*args):
            if args == ("rev-parse", "HEAD"):
                return head
            if args == ("tag", "--list"):
                return "v0.3.28\nv0.3.29\nv0.3.30\nv0.4.0-beta.1"
            return "broken"  # Existing failed tags point at old code.
        with patch.object(release, "git", side_effect=git), \
                patch.object(release, "successful_baseline", return_value="v0.3.28"), \
                patch.object(release, "cumulative_changes", return_value=(lines, 0)):
            return release.make_plan(None)

    def test_499_waits_and_500_releases_next_unused_patch(self):
        self.assertFalse(self.plan(499)["should_release"])
        plan = self.plan(500)
        self.assertTrue(plan["should_release"])
        self.assertEqual(plan["base_tag"], "v0.3.28")
        self.assertEqual(plan["next_tag"], "v0.3.31")

    def test_same_commit_retries_existing_tag(self):
        plan = self.plan(500, head="broken")
        self.assertEqual(plan["next_tag"], "v0.3.30")

    def test_partial_release_does_not_reset_counter(self):
        class API:
            def releases(self):
                return [published("v0.3.28"), published("v0.3.30")]

            def latest_run(self, workflow, tag, head):
                return run_info(1, conclusion="failure" if tag == "v0.3.30" and workflow == "docker.yml" else "success")
        with patch.object(release, "is_ancestor", return_value=True), \
                patch.object(release, "git", return_value="sha"):
            self.assertEqual(release.successful_baseline(API(), "head"), "v0.3.28")

    def test_incomplete_or_draft_assets_are_not_a_successful_release(self):
        value = published("v0.3.31")
        self.assertTrue(release.has_assets(value))
        value["assets"].pop()
        self.assertFalse(release.has_assets(value))
        value = published("v0.3.31")
        value["draft"] = True
        self.assertFalse(release.has_assets(value))


class GitCountingTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="vocat-release-test-")
        previous = os.getcwd()
        self.addCleanup(temporary.cleanup)
        self.addCleanup(os.chdir, previous)
        os.chdir(temporary.name)
        self.git("init", "-q", "-b", "master")
        self.git("config", "user.name", "Release Test")
        self.git("config", "user.email", "release@example.test")
        self.git("config", "core.autocrlf", "false")
        Path("app.py").write_text("", encoding="utf-8")
        self.commit("baseline")
        self.git("tag", "v1.0.0")

    def git(self, *args):
        return subprocess.check_output(["git", *args], text=True, stderr=subprocess.DEVNULL).strip()

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-qm", message)

    def test_intermediate_edits_and_reverts_accumulate(self):
        Path("app.py").write_text("x\n" * 300, encoding="utf-8")
        self.commit("add 300")
        Path("app.py").write_text("", encoding="utf-8")
        self.commit("remove 300")
        self.assertEqual(release.cumulative_changes("v1.0.0", "HEAD"), (300, 300))
        self.assertEqual(self.git("diff", "--numstat", "v1.0.0", "HEAD"), "")

    def test_merge_counts_pr_once(self):
        self.git("switch", "-qc", "feature")
        Path("app.py").write_text("x\n" * 250, encoding="utf-8")
        self.commit("feature")
        self.git("switch", "-q", "master")
        self.git("merge", "--no-ff", "-qm", "merge feature", "feature")
        self.assertEqual(release.cumulative_changes("v1.0.0", "HEAD"), (250, 0))

    def test_docs_and_lockfiles_do_not_trigger_release(self):
        for name in ("docs/guide.md", "web/package-lock.json", "web/dist/app.js", "go.sum"):
            path = Path(name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("ignored\n" * 600, encoding="utf-8")
        self.commit("generated and documentation only")
        self.assertEqual(release.cumulative_changes("v1.0.0", "HEAD"), (0, 0))


class PublishAPI:
    def __init__(self, fail=False, delay=False, incomplete=False):
        self.fail, self.delay, self.incomplete = fail, delay, incomplete
        self.posts = []
        self.lookups = 0

    def latest_run(self, workflow, tag, head):
        if workflow == "release.yml":
            return run_info(10)
        if not self.posts:
            return run_info(20, conclusion="failure")
        self.lookups += 1
        if self.delay and self.lookups == 1:
            return run_info(20, conclusion="failure")  # API index has not caught up.
        return run_info(21, "in_progress", None)

    def request(self, path, method="GET", data=None, missing_ok=False):
        if method == "POST":
            self.posts.append((path, data))
            return None
        if path == "actions/runs/10":
            return run_info(10)
        if path == "actions/runs/21":
            return run_info(21, conclusion="failure" if self.fail else "success")
        if path == "releases/tags/v0.3.31":
            value = published("v0.3.31")
            if self.incomplete:
                value["assets"].pop()
            return value
        raise AssertionError(path)


class PublishTests(unittest.TestCase):
    def publish(self, api):
        with patch.object(release, "git", return_value="fixed"), \
                patch.object(release, "ensure_tag") as tag, \
                patch.object(release, "summary"), patch.object(release.time, "sleep"):
            release.publish(api, {"head_sha": "fixed", "next_tag": "v0.3.31", "should_release": True}, poll=0)
            tag.assert_called_once_with("v0.3.31", "fixed")

    def test_only_failed_workflow_is_retried_and_index_lag_is_tolerated(self):
        api = PublishAPI(delay=True)
        self.publish(api)
        self.assertEqual(api.posts, [("actions/workflows/docker.yml/dispatches", {"ref": "v0.3.31"})])

    def test_downstream_failure_is_not_reported_as_success(self):
        with self.assertRaisesRegex(release.ReleaseError, "baseline is unchanged"):
            self.publish(PublishAPI(fail=True))

    def test_missing_binary_is_not_reported_as_success(self):
        with self.assertRaisesRegex(release.ReleaseError, "expected published assets"):
            self.publish(PublishAPI(incomplete=True))

    def test_timeout_resumes_inflight_runs_without_duplicate_dispatch(self):
        api = PublishAPI()
        api.latest_run = lambda *args: run_info(10, "in_progress", None)
        api.request = lambda *args, **kwargs: run_info(10, "in_progress", None)
        with patch.object(release.time, "monotonic", side_effect=[0, 1, 10000]), \
                self.assertRaisesRegex(release.ReleaseError, "resume"):
            self.publish(api)
        self.assertFalse(api.posts)

    def test_existing_tag_cannot_be_moved_to_a_fix_commit(self):
        class Existing:
            returncode = 0
        with patch.object(release, "git", side_effect=["", "other-commit"]) as git, \
                patch.object(release.subprocess, "run", return_value=Existing()), \
                self.assertRaisesRegex(release.ReleaseError, "another commit"):
            release.ensure_tag("v0.3.31", "fixed")
        self.assertEqual(git.call_count, 2)  # Fetch and inspect; never tag or push.


if __name__ == "__main__":
    unittest.main()
