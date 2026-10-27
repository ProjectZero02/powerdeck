# Security

## Reporting

Please report vulnerabilities privately through GitHub's **Security → Report a vulnerability** on this repository. Don't open a public issue.

## Threat model

PowerDeck can shut computers down, wake them, and (through a NanoKVM) press their power buttons. So the design assumes:

- **Other devices on the tailnet** may be hostile, including other devices signed in to your own Tailscale account. A helper fully trusts only its **owner devices**: nodes recorded by scanning its code (which carries a one-time pairing code: 8 characters, 40 bits, 10 minutes, single use, 5 wrong guesses and it's gone) or by `powerdeck-helper owner add` on the machine. `tailscale whois` identifies them on every request. A phone on another account that enters the code gets a key for that device only, without admin: it can power it on and off but can't manage keys, settings or owners, relay to other devices, use MCP, or schedule waking others. Ten failures from one address block it for 10 minutes. The phone pins each helper's certificate.
- **Helpers trust each other only as far as their peer keys** (status, wake and power, bound to one node), which the owner's app sets up. A compromised helper can power the others on and off through them, but can't change their settings, keys or owners, or relay further.
- **AI agents and scripts** may misbehave. Their keys carry only the scopes and devices they were given, can't override someone using the device, always leave a 60-second countdown, are rate-limited (one shutdown per device per 10 minutes, six wakes per hour), can't shut down the device the agent runs on, and are bound to the tailnet node that first uses them. Every action is logged with the key's name.
- **The local network** is not trusted. Helpers listen only on loopback and their Tailscale addresses (never on all interfaces), check that every caller has a tailnet or loopback address, and on Windows the firewall rule allows the Tailscale range only. The MCP port 47323 is plain HTTP for agent tools that can't pin a self-signed certificate: it serves only `/mcp`, is bound to the Tailscale addresses, and Tailscale's WireGuard encrypts it end to end. Prefer `https://<hub>:47321/mcp` where the agent supports it.
- **The phone sends no plain HTTP** except to Tailscale (`*.ts.net`) names, which is how it reaches NanoKVMs (they only speak HTTP). Everything else, including every helper, is TLS.
- **A KVM wired wrongly** could press the wrong button. The app presses to turn on only when the computer's helper doesn't answer and the power light reads off, and only after it has seen that light on (an unwired LED reads off forever). Linking checks that the light agrees with the computer's helper.
- **Other people on the same PC** can't use the PowerDeck window: on Windows it answers only programs of the person signed in at the screen, or elevated administrators. It also needs a per-run key embedded in the page and refuses foreign `Host` headers (DNS rebinding).
- **Websites in a browser** must not drive a helper. Requests carrying an `Origin` header are refused. The Windows window on `127.0.0.1` checks the `Host` header (DNS rebinding) and needs a per-run key embedded only in the page, with a CSP that forbids framing.
- **Release downloads** could be tampered with. `install.sh` checks the SHA-256 and the Ed25519 signature; `install.ps1` checks the SHA-256. The helpers' updaters install only files whose Ed25519 signature verifies against the public key compiled in. Android only accepts updates signed with the original key.
- **The update-signing key is a single point of failure.** Helpers run as SYSTEM/root and install whatever that key signs, so whoever holds it (or can publish a release through the repository's workflow) could run code on every helper that updates automatically. To limit that: a release installs on its own only after it has been public for **72 hours**, so a bad or forged release can be noticed and pulled first; owners can **pause** automatic updates for 7 days; new Linux installs (servers) start with automatic updates **off**; and installing by hand always shows the version first. The key lives only in this repository's Actions secrets (plus an offline backup); there is no key rotation in the helper yet, so a leaked key means shipping a new public key through a release signed with the old one, before the attacker does.
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
- **Upgraded helpers (legacy owners):** a helper upgraded from 2.0 still trusts every untagged device on its Tailscale account until the first one claims it (the updated app does this on its own; the PC window shows a warning until then). Update the app before or together with the helpers: the 2.0 app can't pair with a 2.1 helper.
- **A lost phone:** a stolen *unlocked* phone can power your devices on and off, like any remote. Remove it from your Tailscale account, and change the NanoKVM password.
