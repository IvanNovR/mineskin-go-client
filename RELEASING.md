# Releasing

## Versioning

- The module is on v0.x. Moving to v1.0.0 is a manual decision of the owner.
- There is never a v2 or later: a Go v2 module would need the import path suffix `/v2`, which would be confused with the MineSkin "API V2" that this client already targets.
- Conventional Commit types decide the version while on v0: `fix` raises the patch; `feat` and breaking changes raise the minor; `docs`, `test`, `ci`, `chore`, `perf` and the like do not release. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Promotion

1. Run the live tests (see "Live tests" below).
2. Open a pull request from `dev` to `main`, titled with a non-bumping type, for example `chore: promote dev to main`. The merge commit carries this title (the repository's merge commit message is set to the pull request title), so a `feat:` or `fix:` title would change the version.
3. Merge it with a **merge commit**.
4. The release then happens automatically (see "Automation" below). Publishing to the Go module proxy cannot be undone.

## Hotfixes

A hotfix released from `main` (branch `hotfix/*`) is missing on `dev`. Back-sync it by hand: cherry-pick the hotfix commits onto a branch cut from `dev` and open a pull request into `dev`.

## Retracting a version

Never delete or re-tag a published version. Add a `retract vX.Y.Z // reason` directive to `go.mod` and ship it in a new patch release.

## Repository settings (for recovery)

These settings are expected on the GitHub repository:

- Branch ruleset "Protect main": pull requests only, required status checks including `branch-policy` and `release-preview`.
- Merge commit message = pull request title (`merge_commit_title: PR_TITLE`), so the promotion title is the merge commit subject that svu reads and the release preview simulates.
- Tag ruleset for `v*`: no deletion and no update of tags (creation only by GitHub Actions).
- Immutable releases enabled, before the first release (the setting does not apply to earlier releases).
- Dependabot alerts and security updates, and private vulnerability reporting enabled.
- Actions workflow permissions: read-only by default, and Actions cannot approve pull requests.
- Environment `live` (branches `dev` and `main`) holding the secret `MINESKIN_API_KEY`.
- Repository variable `RELEASE_AUTOMATION_READY` (kill switch for the release jobs that write).

## Automation

`.github/workflows/release.yml` runs on every push to `main`:

1. **resolve** (read-only) checks that the run is on `main` and that the commit is on `main`, then runs [svu](https://github.com/caarlos0/svu) v3.4.4 with `.svu.yml` (`v0: true`) to compute the next version. svu runs only here, never in a job that holds a write token.
2. **ci** runs the full CI (`ci.yml`) on that exact commit.
3. **release** (only when CI passed and there is something to release) creates the tag and a GitHub Release with generated notes on that commit (not a draft, not a prerelease; marked latest only when its tag is the highest v0 tag), and then checks the Release, that the tag points at the commit, and that the commit is on `main`.
4. **proxy** asks `proxy.golang.org` for the new version, retrying with backoff for about 9 minutes. If it still fails, the job turns red but the release stays; see "Recovery".

What gets released:

- `fix` raises the patch; `feat` and breaking changes (`!` or a `BREAKING CHANGE:` footer) raise the minor. The workflow refuses any version that is not `v0.x.y`, so it never publishes v1 or later.
- When nothing since the last tag is a `feat`, `fix` or breaking change (for example only `docs`, `test`, `ci`, `chore` or `perf`), the release job reports "No releasable changes" and ends green. CI still runs.
- Several `feat`/`fix` commits in one promotion still raise the version only once.
- svu reads every commit since the last tag, including the commits merged from `dev`. Its matching is case-insensitive and substring-based on the subject line: `feat:`, `fix:` and `<word>!:` count anywhere in it, so `Feat:`, `defeat:` and `test: cover prefix: handling` all count (`prefix:` contains `fix:`). It also reads `BREAKING CHANGE:` (or `BREAKING-CHANGE:`) from the body of every commit, which raises the minor. Keep these words out of non-releasing subjects and bodies.

To see the coming version locally, run `make next-version` on an up-to-date `dev` after `git fetch --tags` (svu needs Go 1.27.3 or later; `go run` downloads that toolchain if needed). The promotion title is not included there; the release preview below includes it.

### Release preview

`release-preview.yml` is a required check on pull requests into `main`. It runs the same svu, with the `.svu.yml` from `main` (the pull request's own copy only while `main` has none, and the summary then says so), on the pull request merged into `main` plus an empty local commit that carries the pull request title, and reports "Merging releases vX.Y.Z" or "No release (...)" in the job summary. It fails if the result is not a `v0.x.y` version. For the promotion to v0.1.0 it must say **Merging releases v0.1.0**; if it says anything else, stop and investigate.

### Trust model

- Anyone with write access to the repository can release: they could dispatch an edited copy of the workflow, so the write power of the workflow's `GITHUB_TOKEN` is not fenced off from them.
- The real protections are the `v*` tag ruleset (tags cannot be deleted or moved) and immutable releases, so a published version can never be changed.
- What this does not prevent: someone with write access can still create a new `v0.*` tag and Release through an edited dispatch. Because tags and releases are immutable and the Go module proxy caches every version permanently, such a version cannot be removed. It can only be retracted (see [Retracting a version](#retracting-a-version)).
- Immutable releases protect only releases published after the setting is turned on, so check that it is enabled before the first release.
- Upgrade path, once anyone besides the owner gets write access: create tags and Releases with a GitHub App token held in an environment secret, and change the tag ruleset so that only that App may create `v*` tags.

### Kill switch

The jobs that write (release and proxy) run only when the repository variable `RELEASE_AUTOMATION_READY` is `true`. With any other value a push to `main` still runs CI and ends green with "Kill switch off (RELEASE_AUTOMATION_READY), nothing was released" in the summary; a manual run fails, because dispatching with the switch off is a mistake. Turn it off with:

```sh
gh variable set RELEASE_AUTOMATION_READY --body false
```

### Recovery

Manual runs need `release.yml` on `main`, so they only work after the first promotion. Always run them on `main`; any other ref is refused. Manual runs share the `release` concurrency group with pushes to `main`: a queued (not yet started) run is replaced by a later push, so check that it ran and re-run it if it was cancelled.

Before a retry, check that the merge commit message setting is still "pull request title" (see "Repository settings"); svu reads merge commit subjects.

- **Missing tag or Release** (for example the release job failed after CI passed): run `retry`. It runs CI on the commit again, computes its version and creates whatever is missing; a tag or Release that already exists is checked, never moved or re-created. Without `sha` it uses the head of `main`.

  ```sh
  gh workflow run release.yml --ref main -f mode=retry
  gh workflow run release.yml --ref main -f mode=retry -f sha=<full 40-character SHA on main> -f tag=vX.Y.Z
  ```

  `tag` is optional and only an assertion: the run is refused when it differs from the version svu computes for that commit (or from the tag already on it). A `tag` given when there is nothing to release is refused too; without `tag` that case just ends green. A commit that is already part of a later version tag is refused. The Release is marked latest only when its tag is the highest v0 tag.

- **Version not on the Go proxy or pkg.go.dev**: run `proxy-only` for the existing tag. It skips CI and the release job.

  ```sh
  gh workflow run release.yml --ref main -f mode=proxy-only -f tag=vX.Y.Z
  ```

- Never delete or re-tag a published version; retract it instead (see "Retracting a version").

## Live tests

The live tests call the real MineSkin API and consume credits. Run them on `dev` before every promotion.

- **First release**: `live.yml` can only be dispatched once it is on the default branch (`main`), so run the live tests locally on an up-to-date `dev` with your own key (see [CONTRIBUTING.md](CONTRIBUTING.md#live-tests)):

  ```sh
  make test-live
  ```

- **Later releases**: run `.github/workflows/live.yml` on `dev` and wait for it to pass:

  ```sh
  gh workflow run live.yml --ref dev -f paid=false
  ```

  Use `-f paid=true` only when the stored key is a paid key; it sets `MINESKIN_LIVE_PAID=1` and also runs the tests that need one.

Setup: the workflow runs in the GitHub Environment `live`, limited to the branches `dev` and `main` with no required reviewer, and reads the environment secret `MINESKIN_API_KEY`. The owner sets the secret himself:

```sh
gh secret set MINESKIN_API_KEY --env live
```

`gh` prompts for the value, so it never lands in shell history. Agents and automation never handle the key. If the secret is missing, the workflow fails at its first check with "MINESKIN_API_KEY secret not configured in environment live"; the key is never printed.
