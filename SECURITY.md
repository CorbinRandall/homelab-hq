# Security Policy

## Supported versions

Security fixes are applied to the latest release.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting for this repository. Do not include credentials, private keys, or sensitive network details in a public issue.

## Deployment guidance

Homelab HQ has no built-in authentication. Restrict it to a trusted LAN or private VPN and do not forward its port from the public Internet. Use a dedicated SSH key with the narrowest practical permissions and keep live configuration, state, and credentials outside the source checkout.
