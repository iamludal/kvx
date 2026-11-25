#!/usr/bin/env bash
# Start a fresh throwaway Vault dev server on :8299 (in-memory, restarted on
# every run) and seed it with sample secrets.
# Usage: source <(scripts/dev-vault.sh)   -> exports VAULT_ADDR / VAULT_TOKEN
set -euo pipefail
export VAULT_ADDR=http://127.0.0.1:8299 VAULT_TOKEN=kvx-dev
pkill -f 'dev-listen-address=127.0.0.1:8299' 2>/dev/null && sleep 0.5 || true
vault server -dev -dev-root-token-id="$VAULT_TOKEN" -dev-listen-address=127.0.0.1:8299 >"${TMPDIR:-/tmp}/kvx-dev-vault.log" 2>&1 &
for _ in $(seq 50); do vault status >/dev/null 2>&1 && break; sleep 0.1; done
vault secrets enable -path=kv1 -version=1 kv >/dev/null 2>&1 || true
vault kv put secret/app/db user=app password=s3cr3t-pass host=db-old.internal >/dev/null
vault kv put secret/app/db user=app password=n3w-s3cr3t-pass host=db-old.internal >/dev/null
vault kv put secret/app/api API_KEY=abcdef123456 timeout=30 >/dev/null
vault kv put secret/app/cache url=redis://cache:6379 empty= >/dev/null
vault kv put secret/staging/db user=app password=s3cr3t-pass >/dev/null
vault kv put secret/prod/db user=app password=prod-very-secret host=db.prod >/dev/null
vault kv put secret/legacy/old token=leaked-token-123 >/dev/null
vault kv delete secret/legacy/old >/dev/null
vault kv put kv1/team/a apiKey=abcdef123456 >/dev/null
echo "export VAULT_ADDR=$VAULT_ADDR VAULT_TOKEN=$VAULT_TOKEN"
