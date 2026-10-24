#!/usr/bin/env python3
"""Release every 500 cumulative source-line changes, counting successful releases."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

WORKFLOWS = ("release.yml", "docker.yml")
ASSETS = {"SHA256SUMS", *("vocat-linux-" + arch for arch in ("386", "amd64", "arm64", "aarch64", "armv7"))}
EXCLUDED = ("README.md", "docs/**", "img/**", "LICENSE", "LICENSES/**", "NOTICE",
            "go.sum", "web/package-lock.json", "web/pnpm-lock.yaml", "web/public/**", "web/dist/**")


class ReleaseError(RuntimeError):
    pass


def git(*args):
    return subprocess.check_output(["git", *args], text=True, encoding="utf-8").strip()


def version(tag):
    match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", tag)
    return tuple(map(int, match.groups())) if match else None


def is_ancestor(tag, head):
    return subprocess.run(["git", "merge-base", "--is-ancestor", tag, head],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def cumulative_changes(base, head):
    # First-parent merge diffs count merged PR changes once; intermediate edits
    # and reverts still count, rather than disappearing from a final tree diff.
    output = git("log", "--first-parent", "--diff-merges=first-parent", "--format=",
                 "--numstat", f"{base}..{head}", "--", ".",
                 *(":(exclude)" + path for path in EXCLUDED))
    additions = deletions = 0
    for line in output.splitlines():
        fields = line.split("\t", 2)
        if len(fields) == 3 and fields[0].isdigit() and fields[1].isdigit():
            additions += int(fields[0])
            deletions += int(fields[1])
    return additions, deletions


class GitHub:
    def __init__(self):
        repo = os.environ["GITHUB_REPOSITORY"]
        self.base = os.environ.get("GITHUB_API_URL", "https://api.github.com") + f"/repos/{repo}/"
        self.token = os.environ["GH_TOKEN"]

    def request(self, path, method="GET", data=None, missing_ok=False):
        request = urllib.request.Request(self.base + path, method=method,
                                        data=json.dumps(data).encode() if data is not None else None)
        request.add_header("Authorization", "Bearer " + self.token)
        request.add_header("Accept", "application/vnd.github+json")
        request.add_header("Content-Type", "application/json")
        for attempt in range(3):
            try:
                with urllib.request.urlopen(request, timeout=30) as response:
                    payload = response.read()
                    return json.loads(payload) if payload else None
            except urllib.error.HTTPError as error:
                if missing_ok and error.code == 404:
                    return None
                # Do not retry ambiguous writes; a later run can recover them.
                if method == "GET" and error.code in (429, 500, 502, 503, 504) and attempt < 2:
                    time.sleep(2 ** attempt)
                    continue
                raise ReleaseError(f"GitHub {method} {path}: HTTP {error.code}") from error

    def releases(self):
        page = 1
        while True:
            values = self.request(f"releases?per_page=100&page={page}")
            yield from values
            if len(values) < 100:
                return
            page += 1

    def latest_run(self, workflow, tag, head):
        query = urllib.parse.urlencode({"branch": tag, "head_sha": head, "per_page": 100})
        runs = self.request(f"actions/workflows/{workflow}/runs?{query}")["workflow_runs"]
        matching = [run for run in runs if run["head_sha"] == head and run["head_branch"] == tag]
        return max(matching, key=lambda run: run["id"], default=None)


def has_assets(release):
    return (not release.get("draft") and not release.get("prerelease") and
            bool(release.get("published_at")) and ASSETS <= {
                asset["name"] for asset in release.get("assets", [])
                if asset.get("state") == "uploaded" and asset.get("size", 0) > 0})


def successful_baseline(api, head):
    candidates = [release for release in api.releases()
                  if version(release["tag_name"]) is not None and has_assets(release)]
    for release in sorted(candidates, key=lambda item: version(item["tag_name"]), reverse=True):
        tag = release["tag_name"]
        if not is_ancestor(tag, head):
            continue
        sha = git("rev-parse", f"{tag}^{{commit}}")
        runs = [api.latest_run(workflow, tag, sha) for workflow in WORKFLOWS]
        if all(run and run["status"] == "completed" and run["conclusion"] == "success" for run in runs):
            return tag
    raise ReleaseError("No reachable stable release has complete binaries and successful binary/container workflows")


def make_plan(api, threshold=500):
    if threshold <= 0:
        raise ReleaseError("Release threshold must be positive")
    head = git("rev-parse", "HEAD")
    base = successful_baseline(api, head)
    additions, deletions = cumulative_changes(base, head)
    tags = sorted((tag for tag in git("tag", "--list").splitlines() if version(tag) is not None),
                  key=version, reverse=True)
    # Retry an incomplete release at this exact commit. Never move a tag to new
    # code: fixes receive the next number after every existing stable tag.
    pending = next((tag for tag in tags if version(tag) > version(base) and
                    git("rev-parse", f"{tag}^{{commit}}") == head), None)
    major, minor, patch = version(tags[0])
    return {"head_sha": head, "base_tag": base, "next_tag": pending or f"v{major}.{minor}.{patch + 1}",
            "additions": additions, "deletions": deletions, "changed_lines": additions + deletions,
            "threshold": threshold, "should_release": additions + deletions >= threshold or pending is not None}


def summary(text):
    print(text, flush=True)
    if path := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(path, "a", encoding="utf-8") as output:
            output.write(text + "\n")


def ensure_tag(tag, head):
    git("fetch", "--tags", "origin")
    exists = subprocess.run(["git", "show-ref", "--verify", "--quiet", f"refs/tags/{tag}"]).returncode == 0
    if exists:
        if git("rev-parse", f"{tag}^{{commit}}") != head:
            raise ReleaseError(f"Tag {tag} now points to another commit; rerun to recalculate the version")
        return
    git("config", "user.name", "github-actions[bot]")
    git("config", "user.email", "41898282+github-actions[bot]@users.noreply.github.com")
    git("tag", "-a", tag, head, "-m", f"VoCat {tag[1:]}")
    git("push", "origin", f"refs/tags/{tag}")


def publish(api, plan, timeout=3000, poll=15):
    if git("rev-parse", "HEAD") != plan["head_sha"]:
        raise ReleaseError("Checkout changed after release planning")
    if not plan["should_release"]:
        return
    tag, head = plan["next_tag"], plan["head_sha"]
    ensure_tag(tag, head)
    tracked = {}
    for workflow in WORKFLOWS:
        run = api.latest_run(workflow, tag, head)
        if run and (run["status"] != "completed" or run["conclusion"] == "success"):
            tracked[workflow] = {"id": run["id"], "before": 0}
        else:
            tracked[workflow] = {"id": None, "before": run["id"] if run else 0}
            # GITHUB_TOKEN tag pushes do not trigger workflows; dispatch both.
            api.request(f"actions/workflows/{workflow}/dispatches", "POST", {"ref": tag})
    deadline = time.monotonic() + timeout
    previous = {}
    while time.monotonic() < deadline:
        complete = True
        for workflow, state in tracked.items():
            if state["id"] is None:
                run = api.latest_run(workflow, tag, head)
                if run is None or run["id"] <= state["before"]:
                    complete = False
                    continue
                state["id"] = run["id"]
                summary(f"- {workflow}: {run['html_url']}")
            else:
                run = api.request(f"actions/runs/{state['id']}")
            status = (run["id"], run["status"], run["conclusion"])
            if previous.get(workflow) != status:
                print(f"{workflow}: {run['status']} / {run['conclusion']}", flush=True)
                previous[workflow] = status
            if run["status"] != "completed":
                complete = False
            elif run["conclusion"] != "success":
                raise ReleaseError(f"{workflow} failed: {run['html_url']}; the successful release baseline is unchanged")
        if complete:
            release = api.request(f"releases/tags/{tag}", missing_ok=True)
            if not release or not has_assets(release):
                raise ReleaseError(f"{tag}: release workflow finished without all expected published assets")
            summary(f"\nPublished and verified **{tag}**: {release['html_url']}\n"
                    "Both binary and container workflows succeeded. The source-line counter now starts here.")
            return
        time.sleep(poll)
    raise ReleaseError(f"Timed out waiting for {tag}; a later run will resume its existing workflows")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("plan", "publish"))
    parser.add_argument("--plan", required=True, type=Path)
    args = parser.parse_args()
    api = GitHub()
    if args.command == "plan":
        plan = make_plan(api, int(os.environ.get("RELEASE_THRESHOLD", "500")))
        args.plan.write_text(json.dumps(plan, indent=2) + "\n", encoding="utf-8")
        summary(f"### Automatic release threshold\n\n- Successful baseline: `{plan['base_tag']}`\n"
                f"- Target commit: `{plan['head_sha']}`\n- Cumulative additions: {plan['additions']}\n"
                f"- Cumulative deletions: {plan['deletions']}\n"
                f"- Changed code lines: **{plan['changed_lines']}/{plan['threshold']}**\n"
                f"- Release required: {str(plan['should_release']).lower()}\n- Next version: `{plan['next_tag']}`")
        if path := os.environ.get("GITHUB_OUTPUT"):
            with open(path, "a", encoding="utf-8") as output:
                output.write(f"should_release={str(plan['should_release']).lower()}\n")
    else:
        publish(api, json.loads(args.plan.read_text(encoding="utf-8")))


if __name__ == "__main__":
    try:
        main()
    except (ReleaseError, subprocess.CalledProcessError, urllib.error.URLError) as error:
        summary(f"::error::{error}")
        raise SystemExit(1)
