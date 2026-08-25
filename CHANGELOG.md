# Changelog

## 1.1.1 - 2026-08-25

- Reworded parity-check shutdown refusals as clear, actionable safety notices.

## 1.1.0 - 2026-08-25

- Embedded the dashboard assets in the binary and removed third-party Go dependencies.
- Added shared caches for Unraid, array, and Shelly status to reduce upstream work across tabs.
- Reduced idle browser polling and paused it entirely while a tab is hidden.
- Added bounded Shelly discovery, direct timeout-controlled SSH, and Alpine-compatible commands.
- Made runtime JSON writes atomic and persisted schedule firing state across restarts.
- Added graceful shutdown, HTTP limits and timeouts, and a production container smoke test.

## 1.0.0 - 2026-08-24

- Initial public release as Homelab HQ.
- Unraid Docker discovery and application links.
- Wake, sleep, clean shutdown, array start, and progress status.
- Recurring power schedules and optional Shelly outlet control.
- Docker Compose and Debian/Proxmox LXC deployment support.
