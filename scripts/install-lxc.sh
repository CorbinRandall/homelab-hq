#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
config_dir=/etc/homelab-hq
data_dir=/var/lib/homelab-hq

if [[ ! -f "$config_dir/config.json" ]]; then
  echo "Missing $config_dir/config.json; copy and edit config.example.json first." >&2
  exit 1
fi

getent group homelab-hq >/dev/null || groupadd --system homelab-hq
id homelab-hq >/dev/null 2>&1 || useradd --system --gid homelab-hq --home-dir "$data_dir" --shell /usr/sbin/nologin homelab-hq
install -d -o homelab-hq -g homelab-hq -m 0750 "$data_dir"

(cd "$repo_dir" && go test ./...)
(cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o homelab-hq .)
install -m 0755 "$repo_dir/homelab-hq" /opt/homelab-hq/homelab-hq
install -m 0644 "$repo_dir/systemd/homelab-hq.service" /etc/systemd/system/homelab-hq.service
chown -R homelab-hq:homelab-hq "$data_dir"
systemctl daemon-reload
systemctl enable --now homelab-hq
systemctl is-active --quiet homelab-hq
echo "Homelab HQ is running."
