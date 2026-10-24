---
name: release
description: Use when cutting a new public release of this app — e.g. "make a new release", "cut a release", "ship it", "publish a build", "download the CI builds and release". Covers the full pipeline: docs staleness check, gofmt/vet/test green, commit and push, dispatching the version-stamped CI matrix, waiting for it to go green, tagging with a semver version, downloading its artifacts, and creating the semver GitHub release with the assets attached.
---

# Cut a Release

Full pipeline from a dirty working tree to a published GitHub release:
verify docs and checks, commit and push, dispatch the CI matrix with the
version to stamp, wait for it to go green, tag the exact commit it
built, download its artifacts, and publish the release. The dispatch
build stamps the version into the binary (`version.go`), which is what
the in-app updater compares against on other installs.

There are three confirmation checkpoints (commit, version bump,
publish). Everything else runs autonomously. `gh` is already
authenticated; the workflow is `.github/workflows/ci.yml` (single `build`
job, matrix over ubuntu/windows/macos, one artifact per OS; only
`workflow_dispatch` — with a required `version` input — and
`pull_request` trigger it, so pushes never build and a release is
exactly one 3-OS run).

## Phase 1 — Docs and checks

1. **Find the last release point:** `git describe --tags --abbrev=0`
   (falls back to nothing on a fresh clone — then ask the user for the
   baseline). Historical tags may use the retired `MM-DD-YYYY` format;
   only semver tags are releasable going forward.
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

## Phase 2 — Version and commit

1. **Pick the next version** from the last semver tag (ignore any
   date-format tags when computing the bump):
   - fix / chore / docs only since the last tag → patch bump (`0.2.0` → `0.2.1`)
   - any feat / UI change / user-facing improvement → minor bump (`0.2.0` → `0.3.0`)
   - breaking change (prefs format, CLI flags removed, server-list schema) → major bump
   - never released a semver tag before → propose `0.2.0` (the committed
     `FyneApp.toml` says `0.1.0`; treat that as the implicit baseline)
2. **Ask the user to confirm the version** along with the commit
   review below. Same-day double releases are fine — just bump patch
   (`0.2.1`); there is no calendar-collision rule anymore.
3. Confirm the branch is `main` and show the user:
   - `git status --short` (pending code + any doc edits from Phase 1)
   - a one-paragraph summary of what's changing
   - the proposed commit message (sentence-case summary in the repo's
     style; `docs:` prefix when the commit is documentation-only)
4. Update `FyneApp.toml` `Version` to the chosen version (cosmetic — the
   CI stamp is authoritative — but keep it in sync).
5. **Ask the user to confirm** the commit message, file set, and
   version, then commit. Pushing happens in Phase 3; a plain push is
   safe (pushes don't trigger builds):
   ```sh
   git add -A
   git commit -m "<message>"
   ```
6. If the tree was already clean and docs needed nothing (nothing to
   commit), skip the commit — but still verify the remote main is
   current (`git status -sb` shows no ahead marker; `git fetch origin`
   if in doubt) so the Phase 3 push can't be rejected.

Record the release SHA (`git rev-parse HEAD`) — the dispatch run, the
tag, and the release must all sit on this exact commit.

## Phase 3 — Push, dispatch the stamped build, wait for CI

1. **Push main** (plain — nothing auto-builds):
   ```sh
   git push origin main
   ```
   - **Push rejected** (remote main moved, etc.): nothing is lost —
     `git fetch origin && git rebase origin/main`, re-run the Phase 1
     gate, then push again. If the upstream changes touch release files,
     show the user before continuing.
2. **Tag collision check** — if `<X.Y.Z>` exists locally (`git tag -l`)
   or on the remote (`git ls-remote --tags origin`), ask the user for a
   different version — do not delete or move existing tags. Checked now
   so we don't build assets a tag can't use; the tag itself is only
   created after the build is green (step 7).
3. **Dispatch the build** on main with the version to stamp:
   ```sh
   gh workflow run .github/workflows/ci.yml -f version=<X.Y.Z>
   ```
   The run executes main's current head, and the input is
   regex-validated in-workflow (`X.Y.Z` only), so a typo fails fast.
4. **Find and watch the run:**
   ```sh
   sleep 5
   gh run list --workflow ci.yml --event workflow_dispatch --limit 3
   # → pick the newest run, then confirm it sits on the release SHA:
   gh run view <RUN_ID> --json headSha --jq .headSha    # must equal <SHA>
   gh run watch <RUN_ID> --exit-status --interval 30
   ```
   `--exit-status` makes the watch exit non-zero if the run fails.
   Builds take 2–15 minutes (Windows legs are slow); the watch streams
   progress. No run within a couple minutes → recheck the list; don't
   proceed on assumption. headSha ≠ release SHA means main moved between
   push and dispatch (shouldn't happen solo) — stop, re-sync, re-tag the
   newer commit's release flow from Phase 2 step 3 onward.
5. **Run fails:** identify the failing OS + step
   (`gh run view <RUN_ID> --log-failed`), report it to the user, and
   **stop** — no download, no tag, no release. Fix forward with a new
   commit on main, push, and re-dispatch with the *same* version (no tag
   exists yet, so the version is still free).
6. **Verify the artifacts** before continuing (don't use
   `gh run view --json artifacts` — gh 2.102.1 rejects that field; the
   REST API works):
   ```sh
   gh api "repos/{owner}/{repo}/actions/runs/<RUN_ID>/artifacts" \
     --jq '.artifacts[] | [.name, .size_in_bytes] | @tsv'
   ```
   Expect exactly: `faceit-tester-linux-amd64.zip`,
   `faceit-tester-windows-amd64.exe.zip`, `faceit-tester-macos.app.zip`,
   all with non-zero sizes. Missing artifact on a green run = packaging
   bug; stop and investigate.
7. **Tag the built commit.** Tags don't trigger CI anymore — this is a
   pointer, and `gh release create --target` pins the release to the
   same SHA regardless:
   ```sh
   git tag <X.Y.Z> <SHA>
   git push origin refs/tags/<X.Y.Z>
   ```

## Phase 4 — Download artifacts and publish

1. **Download the artifact archives** into a scratch dir. `gh api` has no
   `-o` flag, so redirect stdout (it prints the raw body):
   ```sh
   mkdir -p /tmp/faceit-release && cd /tmp/faceit-release
   gh api "repos/{owner}/{repo}/actions/runs/<RUN_ID>/artifacts" \
      --jq '.artifacts[] | [.id, .name] | @tsv' |
   while IFS=$'\t' read -r id name; do
     gh api "repos/{owner}/{repo}/actions/artifacts/${id}/zip" > "${name}.zip"
   done
   ```
   `{owner}/{repo}` comes from `gh repo view --json nameWithOwner
   --jq .nameWithOwner` (currently `ChristianWSmith/faceit-tester`).
   Verify three non-empty `.zip` files landed in the scratch dir —
   compare sizes against the `size_in_bytes` from the artifact listing
   in Phase 3 (they should match exactly; a short file means a
   truncated/HTML error body, not a zip).
2. **Unwrap the artifact zips** — each contains one final release zip
   (CI uploads zips, and the artifact service wraps those in another
   zip). Extract the inner files; these are the release assets, attached
   verbatim:
   ```sh
   for f in *.zip; do unzip -o "$f"; done
   # → faceit-tester-linux-amd64.zip
   #   faceit-tester-windows-amd64.exe.zip
   #   faceit-tester-macos.app.zip
   ```
   Don't re-zip or rename the inner files — attach them byte-for-byte so
   checksums match what CI built (the in-app updater unpacks these).
3. **Sanity-check the linux zip** contains the expected tree and the
   binary carries its exec bit (the updater chmods anyway, but manual
   installers `make install` straight from it):
   ```sh
   unzip -l faceit-tester-linux-amd64.zip | head
   # expect faceit-tester/usr/local/bin/faceit-tester, Makefile, etc.
   unzip -Z faceit-tester-linux-amd64.zip | grep 'usr/local/bin'
   # expect -rwxr-xr-x (or at least a regular file the updater chmods)
   ```
4. **Release notes:** one bullet per commit since the last tag —
   `git log --oneline <last-tag>..<SHA>` condensed to user-facing
   changes (skip pure chore/docs commits unless notable). Show the notes
   to the user along with the version and asset list; **ask for final
   confirmation** before publishing.
5. **Publish:**
   ```sh
   gh release create <X.Y.Z> \
     --title <X.Y.Z> \
     --target <SHA> \
     --notes "<notes>" \
     /tmp/faceit-release/faceit-tester-linux-amd64.zip \
     /tmp/faceit-release/faceit-tester-windows-amd64.exe.zip \
     /tmp/faceit-release/faceit-tester-macos.app.zip
   ```
6. **Verify:** `gh release view <X.Y.Z>` shows the three assets and the
   release is marked Latest. Clean up the scratch dir. Report the
   release URL to the user.

The release is now live for the in-app updater: older semver installs
see the new tag on their next launch (semver unconditionally supersedes
the retired date tags, so no install is stranded).

## Gotchas

- **Never release from a red or unpushed tree.** Phase 1 and 2 gate
  everything; the shipped binaries must come from the green dispatch run
  sitting on the release SHA.
- Tag/title format is strictly semver `X.Y.Z`, zero `v` prefix.
- **The dispatch run is the one that ships:** its `version` input is
  stamped into `version.go` before compiling. Never publish artifacts
  from a pull-request run or any other unstamped build (those say
  `version = "dev"` and would never update-check), and never dispatch
  with a version other than the one the tag and release will use — the
  stamped binary and the tag must agree.
- The macOS artifact is a zipped `.app` *directory* (not a single
  file); the Windows artifact is the GUI-subsystem exe (`-H
  windowsgui`) zipped by CI. The updater needs these exact layouts —
  if the packaging steps in `ci.yml` change, update `stagePayload` in
  `update.go` and its tests in the same commit.
- `gh run watch` without `--exit-status` exits 0 even on failure —
  always pass it, or a failed build silently becomes a "successful"
  wait.
- **gh CLI quirks (seen on 2.102.1):** `gh run view --json artifacts`
  errors ("Unknown JSON field") — list artifacts via
  `gh api .../runs/<ID>/artifacts` instead. `gh api` has no `-o` flag —
  download binary bodies by redirecting stdout (`... > file.zip`), and
  sanity-check the byte size against the artifact's `size_in_bytes` so
  an error page doesn't get saved as a fake zip.
- **A failed run costs nothing but time:** no tag exists until the
  build is green, so fix forward and re-dispatch the *same* version.
  Never move or delete a published tag.
- This skill commits, pushes, dispatches, tags, and publishes by design,
  but only after the user confirms the commit + version in Phase 2 — and
  publishes only after the Phase 4 confirmation. Nothing public happens
  without those two yeses.
