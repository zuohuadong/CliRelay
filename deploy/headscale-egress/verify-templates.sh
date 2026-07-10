#!/bin/sh
set -eu

base_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
unit="$base_dir/clirelay-codex.service"
firewall_unit="$base_dir/clirelay-codex-firewall.service"
nft_template="$base_dir/clirelay-codex.nft.in"
config_template="$base_dir/config.codex-only.example.yaml"

require_text() {
  file=$1
  text=$2
  if ! grep -Fq "$text" "$file"; then
    echo "missing required text in $file: $text" >&2
    exit 1
  fi
}

for banned in '100.64.0.0/10' '127.0.0.0/8' 'ip6 daddr ::1' 'udp dport 53' 'tcp dport 53'; do
  if grep -Fq "$banned" "$nft_template"; then
    echo "unsafe broad allowance found in nftables template: $banned" >&2
    exit 1
  fi
done

require_text "$nft_template" 'type ipv4_addr . inet_service'
require_text "$nft_template" 'type ipv6_addr . inet_service'
require_text "$nft_template" '__REPLACE_WITH_NUMERIC_CLIRELAY_CODEX_UID__'
require_text "$nft_template" '__REPLACE_WITH_FIXED_HEADSCALE_IPV4__ . 443'
require_text "$nft_template" 'ct state established,related'
require_text "$nft_template" 'reject with icmpx type admin-prohibited'

require_text "$unit" 'User=clirelay-codex'
require_text "$unit" 'EnvironmentFile=/etc/clirelay-codex/environment'
require_text "$unit" 'ProtectSystem=strict'
require_text "$unit" 'ProtectHome=true'
require_text "$unit" 'NoNewPrivileges=true'
require_text "$unit" 'CapabilityBoundingSet='
require_text "$unit" 'RestrictAddressFamilies=AF_INET AF_INET6'
if grep -Fq 'RestrictAddressFamilies=AF_UNIX' "$unit"; then
  echo "unsafe AF_UNIX allowance found in application unit" >&2
  exit 1
fi
require_text "$unit" ' -config /var/lib/clirelay-codex/config/config.yaml'
require_text "$unit" 'data/egress.db.lock'
require_text "$unit" 'Requires=tailscaled.service clirelay-codex-firewall.service'
require_text "$firewall_unit" 'Before=clirelay-codex.service'
require_text "$firewall_unit" 'CapabilityBoundingSet=CAP_NET_ADMIN'
require_text "$firewall_unit" 'apply-nftables.sh /etc/nftables.d/clirelay-codex.nft'

require_text "$config_template" 'local-endpoint-enabled: false'
require_text "$config_template" 'binding-policy: "exclusive"'
require_text "$config_template" 'endpoint-health-ttl: "5m"'
require_text "$config_template" 'probe-urls:'
require_text "$config_template" '__REPLACE_WITH_PRIMARY_IP_ECHO_HOST__'
require_text "$config_template" '__REPLACE_WITH_SECONDARY_IP_ECHO_HOST__'
require_text "$config_template" 'node-freshness-ttl: "3m"'
require_text "$config_template" 'disable-auto-update-panel: true'
require_text "$config_template" 'plugins:'
require_text "$config_template" '  enabled: false'
require_text "$config_template" 'max-retry-credentials: 1'

echo "deployment templates passed deterministic structural checks"

if command -v systemd-analyze >/dev/null 2>&1 && [ "${VERIFY_SYSTEMD_UNIT:-0}" = 1 ]; then
  systemd-analyze verify "$firewall_unit" "$unit"
fi

if command -v nft >/dev/null 2>&1 && [ -n "${RENDERED_NFT:-}" ]; then
  nft -c -f "$RENDERED_NFT"
fi
