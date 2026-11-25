# kvx design notes

## Decisions

| Topic | Decision | Why |
|---|---|---|
| Language | Go | Single static binary (brew, scratch Docker image), official `hashicorp/vault/api` client, cheap parallelism. |
| Name | `kvx` | `vtk` clashes with VTK (`pip install vtk`). `kvx` does not name Vault, so it fits OpenBao too. |
| Paths | Always include the mount | No ambiguity with several KV mounts; same as `vault kv -mount`-less syntax. |
| KV version | Auto-detected per mount via `sys/internal/ui/mounts/<path>` | Works with non-root tokens. `--kv-version` overrides. |
| Masking | Values masked by default as `***` + 6 hex chars of SHA-256 | Safe for screen sharing and CI logs; equal values stay comparable. `get` is the exception: reading a secret is an explicit ask. |
| Destructive ops | Plan → confirm → local snapshot → apply | `--dry-run` alone is easy to forget; snapshots make `undo` possible. |
| Exit codes | 0 ok, 1 finding, 2 error | Lets `search`, `diff --exit-code`, `audit --exit-code` gate CI. |

## Changes from the original notes

- **Delete** is split into three explicit levels (`rm`, `rm --destroy`,
  `rm --wipe`) that map to KV v2 delete, destroy and metadata delete, plus
  `undelete` and `rollback` to go back.
- **Normalize** was vague: it now means renaming *key names* to a case
  convention, with a collision check (`dbPassword` and `DB_PASSWORD` in the
  same secret). Renaming *paths* is `mv`; changing *values* is `replace`.
- **Audit** "older than 90d" became one check among several: stale, empty,
  duplicate values (rotation risk), deleted-but-not-wiped, too many versions,
  key naming.
- **Export/import**: plaintext dumps of secrets are a liability, so export can
  encrypt with age recipients, and import reads age-encrypted input.
- **Clone** is `cp`, and also works across clusters/namespaces
  (`--to-address`, `--to-namespace`), replacing vault-kv-migrate.
- **Parallelism** is the global `-j/--concurrency` flag (default 16).
- **Docker image** is kept but secondary (CI usage); brew and binaries first.

## Ideas added

- `diff` across versions (`path@3`), trees and clusters, with `--keys-only`.
- `search` scopes (`-p/-k/-v`), regex, `-l` like grep.
- `get -f key` for raw values, `--env` for `eval`, `--meta`.
- `exec` to run a process with secrets injected (no `.env` on disk).
- `edit` in `$EDITOR` with check-and-set, like `kubectl edit`.
- `whoami`/`doctor` for "why can't I see this?" debugging.
- `undo` from local snapshots.
- Shell completion (built in), `--include/--exclude` globs, `--depth`,
  `--timeout`, `NO_COLOR`.

## Mutation engine (`internal/plan`)

Every change is a list of `plan.Op` (write, delete, destroy, wipe, undelete)
run by `plan.Run`:

1. **Plan**: printed as a table, or JSON/YAML with `-o` for review in CI.
   `--dry-run` stops here.
2. **Confirm**: `[y/N]`, or typing `yes` when an op is irreversible in Vault
   (destroy, wipe, any KV v1 delete). Without a TTY, `--yes` is required.
3. **Snapshot**: the state of every target (present + data, absent, or
   soft-deleted) is saved before anything is written. `--no-backup` skips it.
4. **Apply**: ops run in parallel within a phase; phase 1 (mv's source wipes)
   only starts once every phase 0 op succeeded, including read-back checks.
   KV v2 writes use check-and-set with the version seen at plan time.

`undo` turns a snapshot back into ops (restore data, wipe what did not exist,
re-delete what was deleted) and runs them through the same engine, so undo is
undoable (twice = redo).

Known limits:
- Restoring a destroyed or wiped secret brings back its latest data as a new
  version; the older history is gone.
- Snapshots are never pruned yet.
- `cp --all-versions` replays only readable versions (deleted/destroyed ones
  are skipped), so version numbers can differ from the source.

## Open questions

1. **Profiles**: a `~/.config/kvx/config.yaml` with named clusters
   (`--profile prod`) instead of `--to-address` + tokens? Useful for `cp`/`diff`
   across clusters.
2. **Path completion**: dynamic shell completion that LISTs Vault as you type
   `kvx get secret/a<TAB>`. Very nice, but one request per TAB.
3. **Interactive mode**: `kvx browse` with a fuzzy finder (bubbletea) to
   navigate and preview secrets?
4. **Snapshot storage**: snapshots hold plaintext secrets on disk. Encrypt them
   (age key generated on first use), or keep them in Vault itself under a
   `kvx-backups/` path? And a retention policy (`kvx undo --prune 30d`)?
5. **Audit from audit logs**: "unused secrets" needs Vault audit device logs.
   Worth parsing a log file (`kvx audit --audit-log file.json`)?
6. **Rate limiting**: is `-j` enough, or add `--rps` for shared clusters?
7. **Other engines**: stay KV-only, or also list database/PKI roles later?

## Roadmap

1. v0.1: read commands (done), tests against a dev server, release pipeline.
2. v0.2: mutation engine (plan/confirm/snapshot/apply), `cp`, `mv`, `rm`,
   `undelete`, `rollback`, `undo` (done).
3. v0.3: `export`/`import` with age, `exec`, `edit`.
4. v0.4: `replace`, `normalize`, profiles, dynamic completion.
