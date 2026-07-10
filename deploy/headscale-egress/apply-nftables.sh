#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "apply-nftables.sh must run as root" >&2
  exit 1
fi

rendered_file=${1:?"usage: apply-nftables.sh RENDERED_NFT [BACKUP_DIR]"}
backup_dir=${2:-/var/lib/clirelay-codex/firewall-backups}

if grep -q '__REPLACE_' "$rendered_file"; then
  echo "refusing to load an unrendered nftables template" >&2
  exit 1
fi

nft -c -f "$rendered_file"
install -d -m 0700 -o root -g root "$backup_dir"

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_path=$(mktemp -d "$backup_dir/clirelay_codex.$timestamp.XXXXXX")
transaction=$(mktemp)
trap 'rm -f "$transaction"' EXIT HUP INT TERM

if nft list table inet clirelay_codex >"$backup_path/table.nft" 2>/dev/null; then
  printf '%s\n' 'delete table inet clirelay_codex' >"$transaction"
  printf '%s\n' present >"$backup_path/state"
else
  : >"$backup_path/table.nft"
  printf '%s\n' absent >"$backup_path/state"
fi

cat "$rendered_file" >>"$transaction"
nft -c -f "$transaction"
nft -f "$transaction"

echo "applied atomically; rollback backup: $backup_path"
