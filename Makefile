COVERAGE_MIN ?= 80
# Keep in sync with the golangci-lint version in .github/workflows/ci.yml
# (Dependabot does not bump this one).
GOLANGCI_LINT_VERSION ?= v2.15.0
GOLANGCI_LINT = go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
# Keep in sync with the svu version in .github/workflows/release.yml and
# release-preview.yml (Dependabot does not bump these).
SVU_VERSION ?= v3.4.4
SVU = go run github.com/caarlos0/svu/v3@$(SVU_VERSION)
# Keep in sync with the actionlint version in .github/workflows/ci.yml.
ACTIONLINT_VERSION ?= v1.7.12
ACTIONLINT = go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: test race vet tidy lint cover check test-live next-version actionlint

test:
	@go test ./...

race:
	@go test -race -shuffle=on ./...

vet:
	@go vet ./...
	@go vet -tags live ./...

# tidy fails when go.mod or go.sum would change.
tidy:
	@go mod tidy -diff

# lint runs a pinned golangci-lint through `go run`; it adds nothing to go.mod.
lint:
	@$(GOLANGCI_LINT) run ./...

# cover fails when total statement coverage of the library is below
# COVERAGE_MIN. The commitlint tool under internal/tools is a CI helper, not
# library code: it is excluded from the gate and only tested with -race.
cover:
	@pkgs=$$(go list ./...) || exit 1; \
	pkgs=$$(printf '%s\n' "$$pkgs" | grep -v /internal/tools/); \
	go test -race -shuffle=on -covermode=atomic -coverprofile=coverage.out $$pkgs
	@go tool cover -func=coverage.out | awk -v min="$(COVERAGE_MIN)" '{ print } /^total:/ { gsub("%", "", $$3); found = 1; if ($$3 + 0 < min + 0) { printf "coverage %s%% is below %s%%\n", $$3, min; fail = 1 } } END { if (!found) { print "no total line in coverage output"; exit 1 } exit fail }'
	@go test -race -shuffle=on ./internal/tools/...

check: vet tidy lint cover

# test-live runs the live tests (they consume API credits). The tests read
# MINESKIN_API_KEY etc. from the environment and, if unset, from a KEY=VALUE
# .env file that they parse themselves; .env is never run as shell code.
test-live:
	@go test -tags live -race -count=1 -run '^TestLive' ./...

# actionlint checks the GitHub Actions workflows (it also runs shellcheck on
# run: scripts when shellcheck is installed).
actionlint:
	@$(ACTIONLINT)

# next-version prints the version that a merge into main would release now
# (svu with .svu.yml; it equals the current tag when nothing is releasable).
next-version:
	@$(SVU) next
