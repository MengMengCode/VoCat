# Automatic patch releases

Pushes to `master` check the cumulative source changes since the latest
reachable, successfully published stable release. At **500 added + deleted
lines**, VoCat automatically publishes the next `vMAJOR.MINOR.PATCH` version.
The daily scheduled run also recovers interrupted or failed releases.

- Count first-parent commit diffs, including tests and intermediate edits.
  Merged PRs count once; adding and later reverting code still counts both edits.
- Exclude documentation, images, licenses, dependency lockfiles, `go.sum`, and
  generated web assets.
- A successful baseline needs the five Linux binaries, `SHA256SUMS`, and
  successful binary and Docker workflows. A tag alone never resets the counter.
- Before creating a tag, install, test, and build the frontend from its lockfile.
- Increment the highest existing stable tag, including failed versions, to avoid
  overwriting immutable version references. A retry at the exact same commit
  reuses that commit's incomplete tag.
- Reuse successful or running builds; dispatch only absent or failed workflows.
  Wait for both workflows and verify all release assets before reporting success.
  Failed or timed-out runs remain red and can recover on the next push, daily
  schedule, or manual rerun.

The full process is in `.github/workflows/auto-release.yml` and
`scripts/auto-release.py`. The `web` pull-request workflow also builds dependency
updates before merging. Tailwind stays on 3.4.19 until an explicit Tailwind 4
CSS/PostCSS migration is made; Dependabot ignores that incompatible major version.

Run planner/recovery regression tests with:

```sh
python3 -m unittest discover -s scripts -p 'test_auto_release.py' -v
```
