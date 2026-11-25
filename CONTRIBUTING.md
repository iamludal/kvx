# Contributing to kvx

Thanks for your interest! Bug reports, ideas and pull requests are welcome.

## Before you start

- For a bug, open an issue with the command, the output and your Vault/OpenBao
  version. **Never paste real secrets**: use `-o json` without `-S`, values
  stay masked.
- For a new command or flag, open an issue first so we can agree on the design.
  [docs/DESIGN.md](docs/DESIGN.md) explains the choices made so far and lists
  open questions.

## Development setup

You need Go (see `go.mod`) and the [`vault`](https://developer.hashicorp.com/vault/install)
binary for integration tests.

```bash
git clone https://github.com/iamludal/kvx && cd kvx
make build                          # ./bin/kvx
eval "$(scripts/dev-vault.sh)"      # fresh dev Vault on :8299, with sample data
./bin/kvx tree secret/ --keys
```

## Tests

```bash
make test       # unit tests, no Vault needed
make coverage   # unit + integration tests, race detector, fails under 90%
make lint       # gofmt + go vet
```

Integration tests start their own dev server and work under a random path
prefix, so they never touch data you created by hand.

Every change command must go through `plan.Run` (plan, confirm, snapshot,
apply). New commands need integration tests that check both the state in Vault
and the JSON output.

## Pull requests

- One topic per pull request, with tests.
- Run `make lint coverage` before pushing.
- Describe the behaviour change from the user's point of view.

## Code layout

| Path | What |
|---|---|
| `cmd/kvx` | `main` |
| `internal/cli` | cobra commands |
| `internal/vault` | KV v1/v2 client: resolve, list, walk, read, write |
| `internal/plan` | the plan, confirm, snapshot, apply engine |
| `internal/snapshot` | local backups used by `undo` |
| `internal/output` | tables, JSON/YAML, masking, colors |
