# Security

## Reporting

Please report vulnerabilities privately through GitHub's **Security → Report a vulnerability** on this repository. Don't open a public issue.

## Threat model

PowerDeck can shut computers down, wake them, and (through a NanoKVM) press their power buttons. So the design assumes:

- **Other devices on the tailnet** may be hostile. A helper trusts a caller only when `tailscale whois` says it's an untagged node of the same Tailscale user that owns the helper, and not shared in from another tailnet. Anyone else needs a one-time pairing code shown on the device (8 characters, 40 bits, 10 minutes, single use, 5 wrong guesses and it's gone), which gets a per-phone key. Ten failures from one address block it for 10 minutes. The phone pins each helper's certificate.
- **AI agents and scripts** may misbehave. Their keys carry only the scopes and devices they were given, can't override someone using the device, always leave a 60-second countdown, are rate-limited (one shutdown per device per 10 minutes, six wakes per hour), can't shut down the device the agent runs on, and are bound to the tailnet node that first uses them. Every action is logged with the key's name.
- **The local network** is not trusted. Helpers answer Tailscale addresses (and loopback) only, enforced in the helper itself and, on Windows, in the firewall. The MCP port (plain HTTP, for agent tools that can't pin certificates) serves only `/mcp` and only to tailnet addresses; Tailscale's WireGuard encrypts it.
- **Other people on the same PC** can't use the PowerDeck window: on Windows it answers only programs of the person signed in at the screen, or elevated administrators. It also needs a per-run key embedded in the page and refuses foreign `Host` headers (DNS rebinding).
- **Websites in a browser** must not drive a helper. Requests carrying an `Origin` header are refused. The Windows window on `127.0.0.1` checks the `Host` header (DNS rebinding) and needs a per-run key embedded only in the page, with a CSP that forbids framing.
- **Release downloads** could be tampered with. `install.sh` checks the SHA-256 and the Ed25519 signature; `install.ps1` checks the SHA-256. The helpers' updaters install only files whose Ed25519 signature verifies against the public key compiled in. Android only accepts updates signed with the original key.
- **A KVM press toggles power.** The app reads the power LED right before every press and presses only when it confirms the expected state. The 12-second force-off is behind an explicit confirmation.

## Where secrets live

| Secret | Where |
|---|---|
| Helper's TLS private key, issued keys (SHA-256 hashes only), local CLI token | `C:\ProgramData\PowerDeck` (SYSTEM and Administrators only) or `/var/lib/powerdeck` (root only) |
| Phone's helper keys (other accounts only), NanoKVM passwords | Encrypted with an AES-256-GCM key in the Android Keystore (non-exportable). App backups are disabled |
| Update-signing key, APK signing key | GitHub Actions secrets of this repository (and the maintainer's offline backup) |

## Known limits

- **NanoKVM password encoding:** the NanoKVM's login uses a publicly known key, so its password is only as private as the channel. PowerDeck only talks to the NanoKVM over Tailscale. Anyone holding the NanoKVM password has console access to that computer; keep it in a password manager.
- **Unsigned installer:** the Windows installer is not Authenticode-signed (SmartScreen warns on first run). Integrity of automatic updates relies on the Ed25519 signature described above.
- **Same-account trust:** every untagged device signed in to your Tailscale account is trusted as you. Don't sign other people's devices in to your account; share devices with them instead (shared devices need a pairing code).
- **A lost phone:** a stolen *unlocked* phone can power your devices on and off, like any remote. Remove it from your Tailscale account, and change the NanoKVM password.
