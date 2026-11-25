.PHONY: build test integration lint dev-vault release-snapshot

build:
	go build -o bin/kvx ./cmd/kvx

test:
	go test ./...

# Needs the vault binary. Starts a seeded dev server on :8299.
integration:
	eval "$$(scripts/dev-vault.sh)" && KVX_INTEGRATION=1 go test ./...

lint:
	test -z "$$(gofmt -l .)" && go vet ./...

dev-vault:
	@scripts/dev-vault.sh

release-snapshot:
	goreleaser release --snapshot --clean
