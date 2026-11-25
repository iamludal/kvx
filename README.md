<!-- TODO: banner logo (docs/banner.svg) -->
<h1 align="center">kvx</h1>

<p align="center"><b>Power tools for Vault and OpenBao KV secrets.</b><br>
Browse, search, diff, audit and refactor whole trees of secrets, safely.</p>

<p align="center">
  <a href="https://github.com/iamludal/kvx/actions/workflows/ci.yml"><img src="https://github.com/iamludal/kvx/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/iamludal/kvx/releases"><img src="https://img.shields.io/github/v/release/iamludal/kvx" alt="Release"></a>
  <a href="https://goreportcard.com/report/github.com/iamludal/kvx"><img src="https://goreportcard.com/badge/github.com/iamludal/kvx" alt="Go Report"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/iamludal/kvx" alt="License"></a>
</p>

The `vault` CLI works one secret at a time. `kvx` works on **trees**: it walks
directories in parallel, understands KV v1 and v2, and makes bulk changes with
a plan, a confirmation and an automatic backup.

```console
$ kvx tree secret/app --keys
secret/app/
├── api
│   ├─ API_KEY
│   └─ timeout
└── db
    ├─ host
    ├─ password
    └─ user

0 directories, 2 secrets

$ kvx diff secret/staging/ secret/prod/
--- secret/staging
+++ secret/prod
~ db
    + host = ***c8cebb
    ~ password: ***c1519e → ***cceec9
```

## Contents

- [Features](#features)
- [Installation](#installation)
- [Usage](#usage)
- [Safety model](#safety-model)
- [Examples](#examples)
- [Status](#status)

## Features

| | Command | What it does |
|---|---|---|
| ✅ | `ls` | List a directory, `-l` with versions and dates, `-r` flat recursive |
| ✅ | `tree` | Tree view, with `--keys` / `--values`, depth, globs, JSON output |
| ✅ | `get` | Read a secret or a version (`path@3`), `-f key` raw, `--env`, `--meta` |
| ✅ | `search` | Find a pattern in paths, keys or values (substring or regex) |
| ✅ | `diff` | Secrets, trees, versions, or two clusters (`--to-address`) |
| ✅ | `audit` | Stale, empty, duplicated, deleted, over-versioned, badly named secrets |
| ✅ | `whoami` | Address, identity, policies, TTL, visible KV mounts |
| ✅ | `cp` / `mv` | Copy or move trees across mounts, namespaces and clusters, with history and conflict modes |
| ✅ | `rm` | Soft delete, `--destroy` versions, or `--wipe` metadata |
| ✅ | `undelete` / `rollback` | Restore soft-deleted versions, or re-publish an old one |
| 🚧 | `replace` | Search and replace in values or key names across a tree |
| 🚧 | `normalize` | Rename keys to a convention (`UPPER_SNAKE`...) with collision checks |
| 🚧 | `edit` | Edit a secret in `$EDITOR`, protected by check-and-set |
| 🚧 | `export` / `import` | JSON, YAML, `.env`; optional [age](https://age-encryption.org) encryption |
| 🚧 | `exec` | Run a command with secrets as environment variables |
| ✅ | `undo` | Restore the snapshot taken before the last change |

## Installation

```bash
brew install iamludal/tap/kvx
```

```bash
go install github.com/iamludal/kvx/cmd/kvx@latest
```

```bash
docker run --rm -e VAULT_ADDR -e VAULT_TOKEN ghcr.io/iamludal/kvx tree secret/
```

Or download a binary from the [releases](https://github.com/iamludal/kvx/releases).

## Usage

kvx reads the same environment as the `vault` CLI: `VAULT_ADDR`,
`VAULT_TOKEN` (or `~/.vault-token`), `VAULT_NAMESPACE`, `VAULT_CACERT`...

Paths always start with the mount: `secret/app/db`. A trailing `/` marks a
directory, `@N` selects a KV v2 version. The KV version is detected per mount.

Global flags:

| Flag | |
|---|---|
| `-o, --output` | `table` (default), `json`, `yaml` |
| `-S, --show-secrets` | print values; otherwise they are masked as a short hash (`***c1519e`), so equal values stay recognisable |
| `-j, --concurrency` | parallel requests (default 16) |
| `-n, --namespace` | Vault Enterprise / OpenBao namespace |
| `--kv-version` | force KV v1 or v2 instead of auto-detection |
| `--timeout` | abort after a duration |

Tree commands also take `--depth`, `--include` and `--exclude` (globs on the
relative path, `**` crosses directories).

Exit codes: `0` ok, `1` "found something" (`search` with no match, `diff` and
`audit` with `--exit-code`), `2` error.

## Safety model

Every command that changes data will:

1. print a **plan** of what will change (`--dry-run` stops here),
2. ask for **confirmation** (`--yes` to skip; required without a TTY),
3. **snapshot** every affected secret locally before writing (`--no-backup` to skip),
4. apply in parallel and report what was done.

`kvx undo` restores the last snapshot (`kvx undo --list` to pick another).
Writes use KV v2 check-and-set, so a secret modified by someone else between
the plan and the apply is not overwritten. `mv` wipes sources only after every
destination has been written and read back.

Snapshots contain plaintext values. They are stored in
`$KVX_BACKUP_DIR` or `~/.local/state/kvx/backups` (0600, 0700 directory).

```console
$ kvx rm -r secret/sandbox/ --wipe
wipe  secret/sandbox/a  metadata and 3 versions
wipe  secret/sandbox/b  metadata and 1 version

Plan: 2 wipe.

Warning: 2 of these changes cannot be undone in Vault (kvx keeps the latest data in its backup).
Type yes to apply: yes
backup 20261125-101203.412-9f2c1a (~/.local/state/kvx/backups/...)
✓ 2 changes applied.

$ kvx undo
```

## Examples

```bash
# Which secrets still point to the old database host?
kvx search secret/ db-old.internal -v -l

# Is a leaked token stored anywhere? (matched by value, never printed)
kvx search secret/ 'leaked-token-123' -v

# Same keys in staging and prod?
kvx diff secret/staging/ secret/prod/ --keys-only

# What changed between two versions?
kvx diff secret/app/db@3 secret/app/db@5 -S

# Did the migration to the new cluster copy everything?
kvx diff secret/ secret/ --to-address https://vault-new:8200 --exit-code

# Fail CI when secrets are older than 90 days or reuse the same value
kvx audit secret/team/ --check stale,duplicate --older-than 90d --exit-code

# Copy a tree to a new path, keeping history; re-runs skip identical secrets
kvx cp secret/app/ secret/app-v2/ --all-versions

# Migrate to another cluster, merging into what is already there
kvx cp secret/ secret/ --to-address https://vault-new:8200 --on-conflict merge

# Rename, keeping versions and metadata
kvx mv secret/team/old-name secret/team/new-name

# Clean up a sandbox: review first, then apply
kvx rm -r secret/sandbox/ --wipe --older-than 30d --dry-run

# Back to an older version
kvx rollback secret/app/db@4

# Load a secret into the current shell
eval "$(kvx get secret/app/db --env)"

# Copy a value to the clipboard
kvx get secret/app/db -f password | pbcopy
```

## Status

Early development: read commands and `cp`, `mv`, `rm`, `undelete`,
`rollback`, `undo` work; `replace`, `normalize`, `edit`, `export`, `import` and
`exec` are designed but not implemented yet. See [docs/DESIGN.md](docs/DESIGN.md) for the roadmap and
open questions.

Inspired by [vault-cli](https://github.com/peopledoc/vault-cli),
[vht](https://github.com/ilijamt/vht),
[vault-kv-search](https://github.com/xbglowx/vault-kv-search) and
[vault-kv-migrate](https://github.com/drewmullen/vault-kv-migrate).

kvx is not affiliated with HashiCorp. "Vault" is a trademark of HashiCorp.
