<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/banner-dark.svg">
    <img src="docs/banner-light.svg" alt="kvx: power tools for Vault and OpenBao KV secrets" width="560">
  </picture>
</p>

<p align="center">
  <b>Browse, search, diff, audit and refactor whole trees of secrets. Safely.</b>
</p>

<p align="center">
  <a href="https://github.com/iamludal/kvx/actions/workflows/ci.yml"><img src="https://github.com/iamludal/kvx/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/iamludal/kvx/releases"><img src="https://img.shields.io/github/v/release/iamludal/kvx?include_prereleases&sort=semver" alt="Release"></a>
  <a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/iamludal/kvx" alt="Go version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/iamludal/kvx" alt="MIT License"></a>
</p>

<p align="center">
  <a href="#-quick-start">Quick start</a> ·
  <a href="#-features">Features</a> ·
  <a href="#-installation">Installation</a> ·
  <a href="#%EF%B8%8F-safety-model">Safety</a> ·
  <a href="#-examples">Examples</a> ·
  <a href="#-contributing">Contributing</a>
</p>

---

The `vault` CLI works one secret at a time. **kvx works on trees**: it walks
directories in parallel, understands KV v1 and v2 (Vault and OpenBao), keeps
secret values masked by default, and makes bulk changes with a plan, a
confirmation and an automatic backup you can `undo`.

<p align="center">
  <img src="docs/kvx-demo.gif" alt="Demo of kvx in the terminal">
</p>

## 💡 Why kvx?

| | `vault kv` | kvx |
|---|:---:|:---:|
| Recursive listing, tree view | ❌ | ✅ |
| Search in paths, keys **and values** | ❌ | ✅ |
| Diff secrets, versions, trees or clusters | ❌ | ✅ |
| Copy / move whole trees, across clusters | ❌ | ✅ |
| Export / import trees, encrypted with age | ❌ | ✅ |
| Hygiene audit (stale, duplicated, empty…) | ❌ | ✅ |
| Dry run, confirmation and `undo` | ❌ | ✅ |
| Values masked in output by default | ❌ | ✅ |
| Same command for KV v1 and v2 | ❌ | ✅ |

## 🚀 Quick start

kvx uses the same environment as the `vault` CLI:

```bash
export VAULT_ADDR=https://vault.example.com:8200
vault login                       # or export VAULT_TOKEN=...

kvx whoami                        # who am I, which KV mounts can I see?
kvx tree secret/ --keys           # what is in there?
kvx search secret/ postgres       # where is it?
kvx audit secret/                 # what needs cleaning?
```

Paths always start with the mount: `secret/app/db`. A trailing `/` marks a
directory, `@N` selects a KV v2 version (`secret/app/db@3`).

## ✨ Features

### 🔍 Explore

| Command | What it does |
|---|---|
| `ls` | List a directory; `-l` adds versions and dates, `-r` lists everything below |
| `tree` | Tree view with `--keys` or `--values`, depth limit, globs, JSON output |
| `get` | Read a secret or an old version; `-f key` for the raw value, `--env` for your shell, `--meta` |
| `search` | Find a pattern in paths, keys or values (substring or regex), `-l` like grep |
| `diff` | Compare secrets, trees, versions (`path@3`) or two clusters (`--to-address`) |
| `audit` | Report stale, empty, duplicated, deleted, over-versioned or badly named secrets |
| `whoami` | Address, identity, policies, token TTL, visible KV mounts |

### ✏️ Change (plan → confirm → backup → apply)

| Command | What it does |
|---|---|
| `cp` | Copy a secret or a tree, across mounts, namespaces or clusters, with history |
| `mv` | Move: copy, read back, then remove the sources |
| `rm` | Soft delete, `--destroy` versions, or `--wipe` everything |
| `undelete` | Restore soft-deleted versions |
| `rollback` | Publish an old version again as the latest |
| `undo` | Put back what the last kvx change modified |

### 📦 Move data in and out

| Command | What it does |
|---|---|
| `export` | Dump a secret or a tree to JSON, YAML or `.env`, optionally encrypted with [age](https://age-encryption.org) |
| `import` | Load an export (or any JSON, YAML or `.env`) into a path, through the same plan → confirm → backup → apply |

### 🚧 Coming next

`replace` (search and replace across a tree), `normalize` (rename keys to a
convention), `edit` (in `$EDITOR`, with check-and-set) and `exec` (run a
command with secrets as environment variables). See the
[roadmap](docs/DESIGN.md#roadmap). These commands appear in CLI help but
currently return a "not implemented yet" error.

## 📦 Installation

🍺 **Homebrew** (macOS and Linux):

```bash
brew install iamludal/tap/kvx
```

🐳 **Docker**:

```bash
docker run --rm -e VAULT_ADDR -e VAULT_TOKEN ghcr.io/iamludal/kvx tree secret/
```

📥 **Prebuilt binaries** for Linux, macOS and Windows (amd64 and arm64) are on
the [releases page](https://github.com/iamludal/kvx/releases). Each archive
has a signed build provenance attestation:

```bash
gh attestation verify kvx_*.tar.gz --repo iamludal/kvx
```

🐹 **From source**, with Go:

```bash
go install github.com/iamludal/kvx/cmd/kvx@latest
```

### ⌨️ Shell completion

kvx can complete commands and flags with <kbd>Tab</kbd>. Pick your shell:

<details>
<summary><b>zsh</b> (default on macOS)</summary>

Make sure `compinit` is enabled and load completions from a directory you own.
Add this to `~/.zshrc` (the `fpath` line must come **before** `compinit`):

```bash
fpath=(~/.zsh/completions $fpath)
autoload -U compinit && compinit
```

Then generate the completion file and reload your shell:

```bash
mkdir -p ~/.zsh/completions
kvx completion zsh > ~/.zsh/completions/_kvx
exec zsh
```

Installed with Homebrew? Use `$(brew --prefix)/share/zsh/site-functions/_kvx`
as the destination instead; it is already in your `fpath`.

If completion doesn't show up, clear the cache: `rm -f ~/.zcompdump*`, then
`exec zsh`.

</details>

<details>
<summary><b>bash</b></summary>

Requires the [bash-completion](https://github.com/scop/bash-completion)
package (`brew install bash-completion@2` on macOS).

```bash
# Linux
kvx completion bash > /etc/bash_completion.d/kvx

# macOS (Homebrew)
kvx completion bash > "$(brew --prefix)/etc/bash_completion.d/kvx"
```

Open a new terminal afterwards.

</details>

<details>
<summary><b>fish</b></summary>

```bash
kvx completion fish > ~/.config/fish/completions/kvx.fish
```

</details>

<details>
<summary><b>PowerShell</b></summary>

```powershell
kvx completion powershell | Out-String | Invoke-Expression
```

Add that line to your `$PROFILE` to make it permanent.

</details>

Run `kvx completion --help` for more details.

## ⚙️ Configuration

kvx reads the standard variables: `VAULT_ADDR`, `VAULT_TOKEN`,
`VAULT_NAMESPACE`, `VAULT_CACERT`, `VAULT_SKIP_VERIFY`…

For authentication, a non-empty `--token` takes precedence over `VAULT_TOKEN`.
If neither is set, kvx reads the Vault CLI token helper, so `vault login`
works without exporting a token. The default helper reads `~/.vault-token`;
a custom `token_helper` in `~/.vault` (or the file specified by
`VAULT_CONFIG_PATH`) is invoked with `get`. Helpers inherit the environment:
if a helper selects tokens using `VAULT_ADDR`, set that variable even when
using `--address`. Helper configuration or execution errors are reported
without printing helper output; an empty helper result is treated as no token.
The default token file is stored unencrypted by Vault.

| Global flag | |
|---|---|
| `-o, --output` | `table` (default), `json` or `yaml` |
| `-S, --show-secrets` | print values; otherwise they show as a short hash (`***c1519e`), so equal values stay recognisable |
| `-j, --concurrency` | parallel requests (default 16) |
| `-n, --namespace` | Vault Enterprise / OpenBao namespace |
| `--kv-version` | force KV v1 or v2 instead of auto-detection |
| `--timeout` | abort after a duration (`30s`, `5m`) |
| `--no-color` | no colors (`NO_COLOR` works too) |
| `--no-progress` | no progress bars (also off when stderr is not a terminal, or `TERM=dumb`) |

Commands that walk a tree also take `--depth`, `--include` and `--exclude`
(globs on the relative path, `**` crosses directories).

⏳ On large trees, kvx shows its progress on stderr (listing, reading,
planning, backing up, applying) and erases it when done. It only appears on
a terminal and after a moment, so quick commands, pipes, `-o json` and CI logs
are unchanged.

**Exit codes:** `0` ok · `1` something found (`search` without match, `diff`
and `audit` with `--exit-code`) · `2` error. Handy in CI.

## 🛡️ Safety model

Every command that changes data:

1. 📋 prints a **plan** of what will change (`--dry-run` stops here, `-o json` for review in CI),
2. ✋ asks for **confirmation**: `y`, or typing `yes` when Vault cannot undo it (`--yes` to skip, required without a terminal),
3. 💾 saves a **snapshot** of every affected secret (`--no-backup` to skip),
4. ⚡ applies in parallel and reports what was done.

Writes use KV v2 **check-and-set**: a secret someone changed between the plan
and the apply is never overwritten. `mv` removes sources only after every
destination has been written and read back.

```console
$ kvx rm -r secret/sandbox/ --wipe
wipe  secret/sandbox/a  metadata and 3 versions
wipe  secret/sandbox/b  metadata and 1 version

Plan: 2 wipe.

Warning: 2 of these changes cannot be undone in Vault (kvx keeps the latest data in its backup).
Type yes to apply: yes
backup 20261125-101203.412-9f2c1a (~/.local/state/kvx/backups/...)
✓ 2 changes applied.

$ kvx undo        # changed your mind? (run it twice to redo)
```

> [!WARNING]
> Snapshots hold secret values in plaintext, in `$KVX_BACKUP_DIR` or
> `~/.local/state/kvx/backups` (readable only by you). See [SECURITY.md](SECURITY.md).

## 📚 Examples

```bash
# 🔎 Which secrets still point to the old database host?
kvx search secret/ db-old.internal -v -l

# 🚨 Is a leaked token stored anywhere? (matched by value, never printed)
kvx search secret/ 'leaked-token-123' -v

# ⚖️ Same keys in staging and prod?
kvx diff secret/staging/ secret/prod/ --keys-only

# 🕰️ What changed between two versions?
kvx diff secret/app/db@3 secret/app/db@5 -S

# ✅ Did the migration to the new cluster copy everything?
kvx diff secret/ secret/ --to-address https://vault-new:8200 --exit-code

# 🧹 Fail CI when secrets are older than 90 days or share a value
kvx audit secret/team/ --check stale,duplicate --older-than 90d --exit-code

# 📂 Copy a tree with its history (re-runs skip identical secrets)
kvx cp secret/app/ secret/app-v2/ --all-versions

# 🚚 Migrate to another cluster, merging into what is there
kvx cp secret/ secret/ --to-address https://vault-new:8200 --on-conflict merge

# 🏷️ Rename, keeping versions and metadata
kvx mv secret/team/old-name secret/team/new-name

# 🗑️ Clean up a sandbox: review first, then apply
kvx rm -r secret/sandbox/ --wipe --older-than 30d --dry-run

# 🔐 Encrypted backup of a whole mount, and a partial restore
kvx export secret/ --format yaml --age-recipient age1... --file backup.yaml.age
kvx import secret/ backup.yaml.age --age-identity key.txt --include 'app/**' --on-conflict skip

# 📄 Seed a secret from a .env file, or write one for local development
kvx import secret/app/config .env --on-conflict merge
kvx export secret/app/config --format dotenv --file .env

# ⏪ Back to an older version
kvx rollback secret/app/db@4

# 🐚 Load a secret into your shell, or copy one value
eval "$(kvx get secret/app/db --env)"
kvx get secret/app/db -f password | pbcopy
```

## 🤝 Contributing

Issues and pull requests are welcome! Read [CONTRIBUTING.md](CONTRIBUTING.md)
to set up a dev Vault and run the tests (`make coverage`), and
[docs/DESIGN.md](docs/DESIGN.md) for the design choices and open questions.

Found a security issue? Please report it privately, see [SECURITY.md](SECURITY.md).

## 🙏 Acknowledgements

kvx builds on ideas from [vault-cli](https://github.com/peopledoc/vault-cli),
[vht](https://github.com/ilijamt/vht),
[vault-kv-search](https://github.com/xbglowx/vault-kv-search) and
[vault-kv-migrate](https://github.com/drewmullen/vault-kv-migrate).

## 📄 License

[MIT](LICENSE) © iamludal

<sub>kvx is not affiliated with HashiCorp or the OpenBao project. "Vault" is a trademark of HashiCorp.</sub>
