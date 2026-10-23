---
name: release
description: Use when cutting a new public release of this app — e.g. "make a new release", "cut a release", "ship it", "publish a build", "download the CI builds and release". Covers the full pipeline: docs staleness check, gofmt/vet/test green, commit and push, tagging with a semver version, waiting for the tag-triggered CI matrix, downloading its artifacts, and creating the semver GitHub release with the assets attached.
---

# Cut a Release

Full pipeline from a dirty working tree to a published GitHub release:
verify docs and checks, commit and push, tag the commit with a semver
version (`X.Y.Z`, no `v` prefix), wait for the tag-triggered CI matrix to
go green, download its artifacts, and publish the release. The tag build
stamps the version into the binary (`version.go`), which is what the
in-app updater compares against on other installs.

There are three confirmation checkpoints (commit, version bump,
publish). Everything else runs autonomously. `gh` is already
authenticated; the workflow is `.github/workflows/ci.yml` (single `build`
job, matrix over ubuntu/windows/macos, one artifact per OS; tags matching
`[0-9]*.[0-9]*.[0-9]*` trigger the same job with the version stamp step
enabled).

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
   version, then commit — but **do not push yet** (Phase 3 pushes the
   commit and the tag together so CI only builds once):
   ```sh
   git add -A
   git commit -m "<message>"
   ```
6. If the tree was already clean and docs needed nothing (nothing to
   commit), skip the commit — but still verify the remote main is
   current (`git status -sb` shows no ahead marker; `git fetch origin`
   if in doubt) so the tag push in Phase 3 won't be rejected.

Record the release SHA (`git rev-parse HEAD`) — the tag is created on
that exact commit in Phase 3.

## Phase 3 — Tag, push together, wait for CI

Create the semver tag locally, then push **main and the tag in one
atomic push**. This matters: a release normally fires two CI events (one
for the branch push, one for the tag), and the tag-triggered run is the
one that ships (it stamps the tag name into `version.go` before
compiling). The branch event's `guard` job skips its matrix when the
pushed SHA is already tagged — but only if the tag is visible
server-side when the branch event is processed. One atomic push makes
that deterministic and a release costs exactly **one** 3-OS build.

**Tag collision:** check before tagging — if `<X.Y.Z>` exists locally
(`git tag -l`) or on the remote (`git ls-remote --tags origin`), ask the
user for a different version — do not delete or move existing tags.

```sh
git tag <X.Y.Z> <SHA>
git push --atomic origin main refs/tags/<X.Y.Z>
```

- **Push rejected** (remote main moved, etc.): nothing was pushed —
  `git fetch origin && git rebase origin/main`, re-run the Phase 1
  gate, re-check the tag collision (the tag itself is still local and
  reusable), then push again. If the upstream changes touch release
  files, show the user before continuing.
- **Clean-tree path** (Phase 2 step 6): main is already pushed and
  current, so the push only carries the tag — the command above still
  works (`main` is a no-op).

Then watch the tag-triggered run. Expect **two** runs for the SHA: the
`main` one has only its guard job (matrix skipped — that's the dedupe
working) and the tag one runs the full matrix; watch the tag one:

```sh
gh run list --commit <SHA> --limit 5
# → pick the run whose head_branch is the tag name; the main run's
#   build job should show "skipped"
gh run watch <RUN_ID> --exit-status --interval 30
```

`--exit-status` makes the watch exit non-zero if the run fails. Builds
take 2–15 minutes (Windows legs are slow); the watch streams progress.

- **Run fails:** identify the failing OS + step
  (`gh run view <RUN_ID> --log-failed`), report it to the user, and
  **stop** — no download, no release. Fix-forward with a new commit on
  main (which does not re-trigger the tag; re-tag the new commit with a
  patch bump after pushing it, e.g. a failed `0.3.0` ships as `0.3.1`).
- **No run appears within a couple minutes:** the push may not have
  landed or CI is queued — recheck with `gh run list --commit <SHA>`;
  don't proceed on assumption.
- The `main` run for the same commit is expected — its `guard` job
  detects the tag and skips the matrix (that's the single-build dedupe;
  if its matrix *ran*, the atomic push was split and the duplicate is
  harmless but wasteful). PR-triggered runs only exist for PR commits —
  pick the run whose `head_branch` equals the tag name.

Verify the run actually produced all three artifacts before continuing
(don't use `gh run view --json artifacts` — gh 2.102.1 rejects that
field; the REST API works):

```sh
gh api "repos/{owner}/{repo}/actions/runs/<RUN_ID>/artifacts" \
  --jq '.artifacts[] | "\(.name)\t\(.size_in_bytes)"'
```

Expect exactly: `faceit-tester-linux-amd64.zip`,
`faceit-tester-windows-amd64.exe.zip`, `faceit-tester-macos.app.zip`,
all with non-zero sizes. Missing artifact on a green run = packaging bug;
stop and investigate.

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
  everything; the release tag must sit on the SHA of the green tag run.
- Tag/title format is strictly semver `X.Y.Z`, zero `v` prefix. The tag
  push *is* the build trigger — `--target` on `gh release create` pins
  the release to the same commit the tag already points at, so the
  release never drifts from its CI run.
- The tag-triggered run is the one that ships: it stamps `version.go`
  with the tag before compiling. Never publish artifacts from the
  branch-push run of the same commit (those say `version = "dev"` and
  would never update-check).
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
- A failed tag run ships as a patch bump (`0.3.0` failed → tag `0.3.1`
  on the fix commit); never move or delete a published tag.
- This skill commits, tags, and pushes by design, but only after the
  user confirms the commit + version in Phase 2 — and publishes only
  after the Phase 4 confirmation. Nothing public happens without those
  two yeses.
