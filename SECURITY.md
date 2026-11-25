# Security policy

kvx handles secrets, so security reports are welcome and taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's
[private vulnerability reporting](https://github.com/iamludal/kvx/security/advisories/new)
instead. You should get a first answer within a few days.

Useful details: kvx version (`kvx --version`), Vault or OpenBao version, the
command you ran, and what was exposed or changed. Never include real secrets.

## What kvx does with your secrets

- It talks only to the Vault address you configure, with your token.
- Values are masked in output unless you pass `--show-secrets`, `get` aside.
- Before every change, kvx saves the affected secrets **in plaintext** to
  `$KVX_BACKUP_DIR` or `~/.local/state/kvx/backups` (files 0600, directory
  0700) so `kvx undo` can restore them. Use `--no-backup` to skip it, and
  delete old snapshots you no longer need.
- Prefer `VAULT_TOKEN` or `~/.vault-token` over `--token`, which ends up in
  your shell history.

## Supported versions

Until 1.0, only the latest release gets fixes.
