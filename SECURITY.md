# Security

## Reporting

Please report vulnerabilities privately through GitHub's **Security → Report a vulnerability** on this repository. Don't open a public issue.

## Threat model

PowerDeck can shut down your PC, and through your NanoKVM it can press your server's power button. So the design assumes:

- **Other devices on the tailnet** may be hostile. Pairing therefore requires a one-time code that's only visible on the PC's screen. Every request after pairing needs a per-phone token, and the phone pins the PC's certificate.
- **The local network** is not trusted. The PC's API answers Tailscale addresses only, both in the Windows Firewall and in the app itself.
- **Websites in the PC's browser** must not be able to drive the PC window on `127.0.0.1`. The window checks the `Host` header (which blocks DNS rebinding), and every API call needs a per-run key. That key is embedded only in the page itself, and a strict CSP forbids framing.
- **Release downloads** could be tampered with. The Windows updater runs an installer only if its Ed25519 signature verifies against the public key compiled into the app. Android only accepts updates signed with the original key.

## Where secrets live

| Secret | Where |
|---|---|
| PC's TLS private key, list of paired phones (token hashes only) | `C:\ProgramData\PowerDeck`, readable by SYSTEM and Administrators only |
| Phone's device token, NanoKVM password | Encrypted with an AES-256-GCM key in the Android Keystore (non-exportable). App backups are disabled |
| Update-signing key, APK signing key | GitHub Actions secrets of this repository (and the maintainer's offline backup) |

## Known limits

- **NanoKVM password encoding:** the NanoKVM's login uses a publicly known key, so its password is only as private as the channel. PowerDeck only talks to the NanoKVM over Tailscale. Anyone holding the NanoKVM password has console access to the server; keep it in a password manager.
- **Unsigned installer:** the Windows installer is not Authenticode-signed (SmartScreen warns on first run). Integrity of automatic updates relies on the Ed25519 signature described above.
- **A lost phone:** a stolen *unlocked* phone with PowerDeck paired can power devices on and off, like any remote. Remove it in the PC window (Paired phones → Remove), and change the NanoKVM password.
