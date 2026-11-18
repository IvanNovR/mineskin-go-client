COVERAGE_MIN ?= 80

.PHONY: test race cover lint test-live

test:
	@go test ./...

race:
	@go test -race ./...

# cover fails when total statement coverage is below COVERAGE_MIN. Until the
# module has any *_test.go file there is nothing to measure, so the target says
# so and succeeds; once a test file exists the gate is always enforced.
cover:
	@go test -race -covermode=atomic -coverprofile=coverage.out ./...
	@if [ -z "$$(find . -name '*_test.go' -print -quit)" ]; then echo "no test files yet: coverage gate skipped"; exit 0; fi; \
	go tool cover -func=coverage.out | awk -v min="$(COVERAGE_MIN)" '{ print } /^total:/ { gsub("%", "", $$3); found = 1; if ($$3 + 0 < min + 0) { printf "coverage %s%% is below %s%%\n", $$3, min; fail = 1 } } END { if (!found) { print "no total line in coverage output"; exit 1 } exit fail }'

lint:
	@golangci-lint run ./...

test-live:
	@test -f .env || { echo ".env missing (copy .env.example)"; exit 1; }; set -a; . ./.env; set +a; go test -tags live -race -count=1 -run '^TestLive' ./...
