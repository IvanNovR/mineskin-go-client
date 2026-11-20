# Releasing

## Versioning

- The module is on v0.x. Moving to v1.0.0 is a manual decision of the owner.
- There is never a v2 or later: a Go v2 module would need the import path suffix `/v2`, which would be confused with the MineSkin "API V2" that this client already targets.
- Conventional Commit types decide the version while on v0: `fix` raises the patch; `feat` and breaking changes raise the minor; `docs`, `test`, `ci`, `chore`, `perf` and the like do not release. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Promotion

1. Run the live tests (see "Live tests" below).
2. Open a pull request from `dev` to `main`, titled with a non-bumping type, for example `chore: promote dev to main`. The merge commit carries this title, so a `feat:` or `fix:` title would change the version.
3. Merge it with a **merge commit**.
4. The release then happens automatically (see "Automation" below). Publishing to the Go module proxy cannot be undone.

## Hotfixes

A hotfix released from `main` (branch `hotfix/*`) is missing on `dev`. Back-sync it by hand: cherry-pick the hotfix commits onto a branch cut from `dev` and open a pull request into `dev`.

## Retracting a version

Never delete or re-tag a published version. Add a `retract vX.Y.Z // reason` directive to `go.mod` and ship it in a new patch release.

## Repository settings (for recovery)

These settings are expected on the GitHub repository:

- Branch ruleset "Protect main": pull requests only, required status checks, the release preview check once it exists.
- Tag ruleset for `v*`: no deletion and no update of tags (creation only by GitHub Actions).
- Immutable releases enabled.
- Dependabot alerts and security updates, and private vulnerability reporting enabled.
- Actions workflow permissions: read-only by default, and Actions cannot approve pull requests.
- Environment `live` (branches `dev` and `main`) holding the secret `MINESKIN_API_KEY`.
- Repository variable `RELEASE_AUTOMATION_READY` (kill switch for the release jobs that write).

## Automation

> Placeholder: the automated release flow, the kill switch and the recovery dispatches are documented here once the release workflow is merged.

## Live tests

> Placeholder: how to run the live tests before a promotion is documented here once the live-test workflow is merged. Until then run `make test-live` locally.
