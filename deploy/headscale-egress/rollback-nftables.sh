#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "rollback-nftables.sh must run as root" >&2
  exit 1
fi

backup_path=${1:?"usage: rollback-nftables.sh BACKUP_DIRECTORY"}
state_file="$backup_path/state"
backup_file="$backup_path/table.nft"

test -r "$state_file"
test -r "$backup_file"

transaction=$(mktemp)
trap 'rm -f "$transaction"' EXIT HUP INT TERM

current_present=false
if nft list table inet clirelay_codex >/dev/null 2>&1; then
  current_present=true
  printf '%s\n' 'delete table inet clirelay_codex' >"$transaction"
fi

case "$(cat "$state_file")" in
  present)
    cat "$backup_file" >>"$transaction"
    ;;
  absent)
    if [ "$current_present" = false ]; then
      echo "rollback already satisfied: table was and is absent"
      exit 0
    fi
    ;;
  *)
    echo "invalid backup state" >&2
    exit 1
    ;;
esac

nft -c -f "$transaction"
nft -f "$transaction"
echo "rollback applied atomically from $backup_path"
