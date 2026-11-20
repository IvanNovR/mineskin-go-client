# Contributing

Thanks for helping. This is a stdlib-only Go client for the MineSkin V2 API.

## Branches and pull requests

- Open pull requests against `dev`. They are squashed or rebased, so `dev` keeps a linear history.
- `main` takes pull requests only from `dev` (or `hotfix/*`), merged with a merge commit; every such merge is released automatically (see [RELEASING.md](RELEASING.md)).
- Dependabot pull requests also target `dev` (see [.github/dependabot.yml](.github/dependabot.yml)).

## Commits and pull request titles

Commit subjects and pull request titles follow
[Conventional Commits](https://www.conventionalcommits.org/): `<type>: <description>`.
Plain text, with no extra markers. Mark a breaking change with `!` (for example
`refactor!:`) or a `BREAKING CHANGE:` footer. Allowed types: feat, fix,
refactor, docs, test, chore, perf, ci, build, style, revert. The commit lint
workflow checks them.

The type decides the next version (the library stays on v0.x for now):

| Type | Effect on the version |
|------|-----------------------|
| `fix` | patch |
| `feat`, or any breaking change | minor |
| `docs`, `test`, `ci`, `chore`, `perf`, and the rest | no release |

## Development

- Write the test first and keep it in the same commit as the code.
- Run `make check` before you push. It runs `go vet` (also with the `live` tag), `go mod tidy -diff`, the pinned golangci-lint and the tests with the race detector, and fails when coverage is below 80%.
- The library uses only the Go standard library. Any new dependency needs the maintainer's approval.

## Live tests

The live tests call the real API and consume credits.

```sh
cp .env.example .env   # then put your key in MINESKIN_API_KEY
make test-live
```

Set `MINESKIN_LIVE_PAID=1` to also run the tests that need a paid key. A real
environment variable overrides `.env`, and `.env` is gitignored. Never commit
a key.

## Security

Report vulnerabilities as described in [SECURITY.md](SECURITY.md), not in public issues.
