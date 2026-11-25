# 🤝 Contributing to kvx

Thanks for your interest! 🙌 Bug reports, ideas and pull requests are welcome.

## 🧭 Before you start

- 🐛 For a bug, open an issue with the command, the output and your Vault/OpenBao
  version. 🔒 **Never paste real secrets**: use `-o json` without `-S`, values
  stay masked.
- 💡 For a new command or flag, open an issue first so we can agree on the design.
  [docs/DESIGN.md](docs/DESIGN.md) explains the choices made so far and lists
  open questions.

## 🛠️ Development setup

You need Go (see `go.mod`) and the [`vault`](https://developer.hashicorp.com/vault/install)
binary for integration tests.

```bash
git clone https://github.com/iamludal/kvx && cd kvx
make build                          # ./bin/kvx
eval "$(scripts/dev-vault.sh)"      # fresh dev Vault on :8299, with sample data
./bin/kvx tree secret/ --keys
```

## 🧪 Tests

```bash
make test       # unit tests, no Vault needed
make coverage   # unit + integration tests, race detector, fails under 90%
make lint       # gofmt + go vet
```

🧱 Integration tests start their own dev server and work under a random path
prefix, so they never touch data you created by hand.

🛡️ Every change command must go through `plan.Run` (plan, confirm, snapshot,
apply). New commands need integration tests that check both the state in Vault
and the JSON output.

## 📬 Pull requests

- 🎯 One topic per pull request, with tests.
- ✅ Run `make lint coverage` before pushing.
- 👤 Describe the behaviour change from the user's point of view.
- 🏷️ **The title must be a [Conventional Commit](https://www.conventionalcommits.org)**:
  pull requests are squash-merged, and the title becomes the commit that
  decides the next version and the changelog entry. A check enforces it.

| Title | Effect (before 1.0) |
|---|---|
| `feat(cp): add --exclude-keys` | ✨ new minor version, listed under Features |
| `fix: mask values in diff --keys-only` | 🐛 new patch version, listed under Bug fixes |
| `feat!: rename --to-address to --to` | 💥 breaking change: new minor version before 1.0, major after |
| `docs:`, `perf:`, `deps:` | 📝 listed in the changelog |
| `refactor:`, `test:`, `ci:`, `chore:`… | 🙈 no release on their own, hidden from the changelog |

## 🚀 Releases

Releases are automated with [release-please](https://github.com/googleapis/release-please)
and [GoReleaser](https://goreleaser.com):

1. 📝 Every merge to `main` updates a `chore: release x.y.z` pull request with
   the next version and `CHANGELOG.md`.
2. 🏷️ Merging that pull request tags the version and creates the GitHub release.
3. 📦 GoReleaser then builds the binaries, the `ghcr.io/iamludal/kvx` image and
   the Homebrew cask, and attaches them to the release with a signed build
   provenance attestation.

🔍 To check the release build locally: `goreleaser release --snapshot --clean`.

## 🗺️ Code layout

| Path | What |
|---|---|
| 🚪 `cmd/kvx` | `main` |
| ⌨️ `internal/cli` | cobra commands |
| 🔐 `internal/vault` | KV v1/v2 client: resolve, list, walk, read, write |
| 📋 `internal/plan` | the plan, confirm, snapshot, apply engine |
| 💾 `internal/snapshot` | local backups used by `undo` |
| 🎨 `internal/output` | tables, JSON/YAML, masking, colors |
