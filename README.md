# Homelab HQ

Homelab HQ is a lightweight dashboard for Unraid Docker applications. It discovers running containers over SSH, builds convenient application links, and provides optional Wake-on-LAN, array, sleep, shutdown, schedule, and Shelly smart-plug controls.

The application is a single Go binary with a static web interface. It is intended to run on an always-on device such as a small Linux host, VM, or Proxmox LXC. If it runs only on the Unraid machine, it cannot wake that machine after shutdown.

## Features

- Discover running Unraid Docker containers and their WebUI links
- Prefer a configurable DNS hostname while retaining LAN links
- Wake, sleep, cleanly shut down, and start the Unraid array
- Show progress for boot, array start, sleep, and shutdown workflows
- Create recurring power schedules in the browser
- Rename or hide discovered applications
- Add manually managed application links
- Optionally control a Shelly outlet through its local RPC API
- Install as a PWA on supported devices

## Docker Compose

Requirements are Docker Compose, network access to Unraid, and SSH key authentication to the Unraid host.

```bash
git clone https://github.com/CorbinRandall/homelab-hq.git
cd homelab-hq
mkdir -p config/keys data
cp config.example.json config/config.json
# Edit config/config.json, then place the Unraid private key at:
# config/keys/unraid_ed25519
chmod 600 config/keys/unraid_ed25519
docker compose up -d --build
```

Open `http://<always-on-host>:8888/`.

The Compose example uses host networking because broadcast Wake-on-LAN is unreliable through ordinary container NAT. Limit dashboard access to your trusted LAN or private VPN; Homelab HQ does not currently provide authentication.

## Configuration

`config.example.json` documents the supported settings with non-routable example addresses. Important fields include:

- `unraid_ip`: address used for local health checks and Docker discovery
- `unraid_url`: browser link to the Unraid WebUI
- `unraid_mac` and `unraid_broadcast`: Wake-on-LAN destination
- `app_hostname`: stable DNS name used in generated application links
- `ssh_target` and `ssh_opts`: SSH discovery and array-control connection
- `sleep_cmd` and `shutdown_cmd`: optional custom commands
- `shelly_host` and `shelly_mac`: optional local smart-plug control
- `header_links`: optional links such as a router, hypervisor, or monitoring page

Never commit the live configuration or keys. `config.json`, `keys/`, `.env`, and `data/` are ignored.

### SSH setup

Create a dedicated key and install its public half for the Unraid root account. Keep the private half readable only by the Homelab HQ service. Confirm non-interactive access before starting the dashboard:

```bash
ssh -i config/keys/unraid_ed25519 -o BatchMode=yes root@192.0.2.10 true
```

The example address above is reserved for documentation; replace it locally.

## Debian or Proxmox LXC

Install Go 1.25 or newer, clone the repository into `/opt/homelab-hq`, and keep private files outside the checkout:

```text
/opt/homelab-hq                 public source and binary
/etc/homelab-hq/config.json     private configuration
/etc/homelab-hq/keys/           private SSH keys
/var/lib/homelab-hq/            schedules, cache, and preferences
```

Adjust `static_dir`, `data_dir`, and the key path in the private configuration for these locations, then run `sudo scripts/install-lxc.sh`. The supplied systemd unit runs as an unprivileged `homelab-hq` user.

## Manual application links

Copy `data.example/static-apps.json` to `static-apps.json` inside the configured data directory. Each entry needs a stable `raw_name`, display `name`, and one or more URLs.

## Development

```bash
cp config.example.json config.json
# Set static_dir to "www" and data_dir to a writable local directory.
go test ./...
go run . -config config.json
```

## Security

Homelab HQ can issue privileged power commands. Run it only on a trusted network, use a dedicated SSH key, restrict that key on the Unraid side where practical, and never expose port 8888 directly to the public Internet. See [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
