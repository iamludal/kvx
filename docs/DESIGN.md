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
  Details in [Export and import](#export-and-import-internaldump).
- **Clone** is `cp`, and also works across clusters/namespaces
  (`--to-address`, `--to-namespace`), replacing vault-kv-migrate.
- **Parallelism** is the global `-j/--concurrency` flag (default 16).
- **Progress** (`internal/progress`): one self-erasing line on stderr, only
  on a terminal and only after 250 ms, so short commands and scripts never
  see it. A spinner and a count while walking (the total is unknown), a bar
  with an ETA for reads, planning, backups and applies. `--no-progress` or
  `TERM=dumb` turn it off. Hand-written rather than a dependency: it is
  ~150 lines and must never write to stdout.
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

## Export and import (`internal/dump`)

- **Layout** (json and yaml): `kvx: 1` (format version), `source`,
  `exported`, then `secret: {data, metadata}` for a single secret or
  `secrets: {<relative path>: {data, metadata}}` for a tree. `metadata`
  (custom_metadata, max_versions, cas_required) is only there with
  `--with-metadata` and when not all defaults. Latest data only: no history.
- **dotenv** is lossy on purpose: names upper-cased like `get --env`, trees
  flattened with a path prefix, collisions and metadata refused (not dropped).
- **import** also reads plain files: a map of paths to objects (a tree), a
  single object, or a `.env` (one secret). Format from `--format`, the file
  extension (`.age`/`.asc` ignored), or the content. Relative paths with
  `..` or empty segments are rejected.
- **import is a change** like `cp`: same `--on-conflict` logic
  (`decideWrite`), check-and-set, snapshot and `undo`. When the data comes
  from stdin, the confirmation is read from `/dev/tty`.
- **age** via `filippo.io/age`: age (`age1…`, `age1pq1…`) and SSH recipients,
  or a recipients file; identities from an age key file or an unencrypted
  SSH key. No plugins or passphrases: pipe through `age -d` for those.
  Binary output is refused on a terminal (`--armor` or redirect). `--file`
  creates the file 0600 and removes it if the export fails half way.

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
3. v0.3: `export`/`import` with age (done), `exec`, `edit`.
4. v0.4: `replace`, `normalize`, profiles, dynamic completion.
