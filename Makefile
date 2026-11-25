.PHONY: build test integration coverage lint dev-vault release-snapshot

# Minimum total statement coverage (unit + integration) enforced by CI.
COVERAGE_MIN ?= 90

build:
	go build -o bin/kvx ./cmd/kvx

test:
	go test ./...

# Needs the vault binary. Starts a seeded dev server on :8299.
integration:
	eval "$$(scripts/dev-vault.sh)" && KVX_INTEGRATION=1 go test ./...

# Unit + integration tests with the race detector, coverage across packages,
# and a failure below COVERAGE_MIN. HTML report: coverage.html.
coverage:
	eval "$$(scripts/dev-vault.sh)" && KVX_INTEGRATION=1 go test -race -count=1 -coverpkg=./internal/... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@scripts/check-coverage.sh coverage.out $(COVERAGE_MIN)

lint:
	test -z "$$(gofmt -l .)" && go vet ./...

dev-vault:
	@scripts/dev-vault.sh

release-snapshot:
	goreleaser release --snapshot --clean
