---
name: release
description: Use when cutting a new public release of this app — e.g. "make a new release", "cut a release", "ship it", "publish a build", "release the latest changes", "download the CI builds and release". Covers the full pipeline: docs staleness check, gofmt/vet/test green, commit and push, waiting for all three CI matrix runners, downloading their artifacts, and creating the MM-DD-YYYY GitHub release with the artifacts attached.
---

# Cut a Release

Full pipeline from a dirty working tree to a published GitHub release:
verify docs and checks, commit and push, wait for the three-runner CI
matrix to go green, download its artifacts, and publish a release named
in `MM-DD-YYYY` format (matching the existing `10-20-2026` /
`10-22-2026` releases — tag = title = date, no `v` prefix).

There are two confirmation checkpoints (commit, publish). Everything else
runs autonomously. `gh` is already authenticated; the workflow is
`.github/workflows/ci.yml` (single `build` job, matrix over
ubuntu/windows/macos, one artifact per OS).

## Phase 1 — Docs and checks

1. **Find the last release point:** `git describe --tags --abbrev=0`
   (falls back to nothing on a fresh clone — then ask the user for the
   baseline).
2. **Review docs for staleness:** diff the tree since that tag
   (`git diff <last-tag>..HEAD`) and check that README.md and AGENTS.md
   still describe reality — feature lists, CLI flags, column order,
   commands, gotchas. Recent changes (new flags, UI behavior, probing
   semantics) that aren't reflected yet must be written in *now*, in the
   existing house style of each file. Don't restructure; patch what's
   stale or missing.
3. **Run the gate, all must pass:**
   ```sh
   gofmt -w . && gofmt -l .    # second command must print nothing
   go vet ./...
   go test -short ./...
   ```
   Fix failures before proceeding. Never release on a red tree.

## Phase 2 — Commit and push

1. Confirm the branch is `main` and show the user:
   - `git status --short` (pending code + any doc edits from Phase 1)
   - a one-paragraph summary of what's changing
   - the proposed commit message (sentence-case summary in the repo's
     style; `docs:` prefix when the commit is documentation-only)
2. **Ask the user to confirm** the commit message and file set, then:
   ```sh
   git add -A
   git commit -m "<message>"
   git push origin main
   ```
3. If the tree was already clean and docs needed nothing (nothing to
   commit), skip the commit — but still verify the remote is current
   (`git status -sb` shows no ahead marker) and use HEAD as the release
   commit.

Record the pushed SHA (`git rev-parse HEAD`) — the release must come
from that exact commit's CI run.

## Phase 3 — Wait for CI

The push triggers one workflow run with a 3-OS matrix; all three legs
must succeed and upload artifacts.

```sh
gh run list --commit <SHA> --branch main --limit 1
# → grab the run ID (the "Build Fyne App" / build job run)
gh run watch <RUN_ID> --exit-status --interval 30
```

`--exit-status` makes the watch exit non-zero if the run fails. Builds
take 2–15 minutes (Windows legs are slow); the watch streams progress.

- **Run fails:** identify the failing OS + step
  (`gh run view <RUN_ID> --log-failed`), report it to the user, and
  **stop** — no download, no release. Fix-forward with a new commit
  (which triggers a fresh run; switch to that run ID).
- **No run appears within a couple minutes:** the push may not have
  landed or CI is queued — recheck with `gh run list --commit <SHA>`;
  don't proceed on assumption.
- Note: PR-triggered runs also exist for the same commit — pick the run
  whose `event` is `push` and branch is `main`.

Verify the run actually produced all three artifacts before continuing:

```sh
gh run view <RUN_ID> --json artifacts --jq '.artifacts[].name'
```

Expect exactly: `faceit-tester-linux-amd64.tar.xz`,
`faceit-tester-windows-amd64.exe`, `faceit-tester-macos.app`. Missing
artifact on a green run = packaging bug; stop and investigate.

## Phase 4 — Download artifacts and publish

1. **Tag name:** `date +%m-%d-%Y` (zero-padded, e.g. `10-23-2026`).
   If `gh release view <TAG>` succeeds, a release already exists for
   today (re-releasing the same day) — **ask the user** how to proceed
   rather than guessing a suffix.
2. **Download the artifact archives** (release assets are the zipped
   artifact archives, matching the `10-20-2026`/`10-22-2026` releases —
   e.g. `faceit-tester-windows-amd64.exe.zip`). Fetch each zip via the
   Actions API into a scratch dir:
   ```sh
   mkdir -p /tmp/faceit-release && cd /tmp/faceit-release
   gh api "repos/{owner}/{repo}/actions/runs/<RUN_ID>/artifacts" \
     --jq '.artifacts[] | [.id, .name] | @tsv' |
   while IFS=$'\t' read -r id name; do
     gh api "repos/{owner}/{repo}/actions/artifacts/${id}/zip" -o "${name}.zip"
   done
   ```
   `{owner}/{repo}` comes from `gh repo view --json nameWithOwner
   --jq .nameWithOwner` (currently `ChristianWSmith/faceit-tester`).
   Verify three non-empty `.zip` files landed in the scratch dir.
3. **Release notes:** one bullet per commit since the last tag —
   `git log --oneline <last-tag>..<SHA>` condensed to user-facing
   changes (skip pure chore/docs commits unless notable). Show the notes
   to the user along with the date and asset list; **ask for final
   confirmation** before publishing.
4. **Publish:**
   ```sh
   gh release create <TAG> \
     --title <TAG> \
     --target <SHA> \
     --notes "<notes>" \
     /tmp/faceit-release/*.zip
   ```
5. **Verify:** `gh release view <TAG>` shows the three assets
   (`faceit-tester-linux-amd64.tar.xz.zip`,
   `faceit-tester-windows-amd64.exe.zip`,
   `faceit-tester-macos.app.zip`) and the release is marked Latest.
   Clean up the scratch dir. Report the release URL to the user.

## Gotchas

- **Never release from a red or unpushed tree.** Phase 1 and 2 gate
  everything; the release target SHA must equal the SHA of the green
  run.
- Tag/title format is strictly `MM-DD-YYYY` with zero padding and no
  `v` prefix — it's a calendar label, not semver. `--target` pins the
  tag to the pushed commit so the release never drifts from its CI run.
- The macOS artifact is a zipped `.app` *directory* (not a single
  file); the Windows artifact is the GUI-subsystem exe (`-H
  windowsgui`). Don't rename or re-zip assets — attach the API zips as
  downloaded so checksums match what CI uploaded.
- `gh run watch` without `--exit-status` exits 0 even on failure —
  always pass it, or a failed build silently becomes a "successful"
  wait.
- Same-day re-releases need a human decision (the calendar tag
  collides); don't invent a scheme on the fly.
- This skill commits and pushes by design, but only after the user
  confirms the commit in Phase 2 — and publishes only after the Phase 4
  confirmation. Nothing public happens without those two yeses.
