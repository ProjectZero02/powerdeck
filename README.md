<p align="center"><img src="helper/assets/icon-256.png" width="96" alt=""></p>

<h1 align="center">PowerDeck</h1>

<p align="center">Turn your computers on and off from your phone, from anywhere.</p>

<p align="center">
  <a href="https://github.com/ProjectZero02/powerdeck/releases/latest"><img src="https://img.shields.io/github/v/release/ProjectZero02/powerdeck" alt="Latest release"></a>
  <a href="https://github.com/ProjectZero02/powerdeck/actions/workflows/build.yml"><img src="https://github.com/ProjectZero02/powerdeck/actions/workflows/build.yml/badge.svg" alt="Build"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/ProjectZero02/powerdeck" alt="MIT license"></a>
</p>

<p align="center">
  <img src="docs/images/home.png" width="200" alt="Three devices with their state and power buttons">
  <img src="docs/images/device.png" width="200" alt="A device's page: setup checks, schedules">
  <img src="docs/images/add-found.png" width="200" alt="Adding devices found on the tailnet">
  <img src="docs/images/home-dark.png" width="200" alt="Dark mode">
</p>

---

PowerDeck has two parts:

- **The helper**, a small background service for **Windows** and **Linux** (including Proxmox). It shuts its computer down or restarts it, sends Wake-on-LAN to other computers on its network, and finds the other PowerDeck devices on your tailnet.
- **The Android app**, listing every device with its state and a power button. It adds a home-screen widget, Quick Settings tiles and notifications.

> [!IMPORTANT]
> **PowerDeck needs [Tailscale](https://tailscale.com)** (free for personal use) on your phone and on every computer, signed in to the same account. That's how your phone reaches your devices from anywhere, and how PowerDeck knows they're yours. Nothing is exposed to the internet.

| | How |
|---|---|
| **Shut down / restart** | The helper on that computer: a clean shutdown with a 60-second countdown you can cancel. If someone is using it (or a game is full-screen), PowerDeck asks first |
| **Turn on** | Wake-on-LAN, sent by another PowerDeck device that's on the same home network (e.g. an always-on server), or by your phone when it's at home |
| **Optional: a [NanoKVM](https://github.com/sipeed/NanoKVM)** wired to the power button | Turns the computer on by pressing the button, even where Wake-on-LAN can't, and is a fallback for shutting down. PowerDeck reads the power light first, so a press never turns a running computer off by mistake |

## Set up

1. **Tailscale**: install it on your phone and your computers, and sign in with the same account.
2. **The helper**: run one line on each computer.

   **Windows**: open PowerShell and paste:
   ```powershell
   irm https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.ps1 | iex
   ```
   (or download and run [PowerDeck-Setup.exe](https://github.com/ProjectZero02/powerdeck/releases/latest/download/PowerDeck-Setup.exe)). A window opens with a QR code.

   **Linux / Proxmox**: in a terminal:
   ```sh
   curl -fsSL https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.sh | sudo sh
   ```
   It prints a QR code (show it again any time with `powerdeck-helper qr`).

   Both installers verify the download before running it, set up everything the computer needs (see [below](#what-the-helper-sets-up)), and tell you about anything they can't fix themselves, such as a BIOS setting.
3. **The app**: install [PowerDeck.apk](https://github.com/ProjectZero02/powerdeck/releases/latest/download/PowerDeck.apk) on your phone (the Windows window has a QR code for it). Open it and tap **Scan the helper's code**.

Your other PowerDeck computers and NanoKVMs then show up under **Found on your tailnet**: tap **Add** for each. A NanoKVM asks for its password and which computer's power button it's wired to.

> **Windows SmartScreen** may warn about the installer because it isn't code-signed yet. Choose **More info → Run anyway**. Every release is built by GitHub Actions from this repository (see [Checking a download](#checking-a-download)).

### Shortcuts

- **Widget**: every device and its state; tap one for a quick on/off dialog.
- **Quick Settings tiles**: up to four; pick a device for each on its page.
- **Home-screen shortcut** per device (device page → ⋮ → Add to home screen), and long-press shortcuts on the app icon.

### On each device's page

- **Setup checks**, with a **Fix** button where PowerDeck can fix it remotely (Wake-on-LAN, Fast Startup, firewall, Tailscale unattended).
- **Schedules**: shut down, restart or wake at set times. Wake schedules are kept by another always-on device on the same network.
- **History**: who did what and when, including schedules and agents.
- **Update and shut down** on Windows when updates are waiting.

## AI agents (MCP)

PowerDeck can let an AI agent or a script check and control your devices through [MCP](https://modelcontextprotocol.io). Open an always-on device in the app (a home server is ideal), tap **Connect an agent**, choose which devices it may use and whether it may shut them down, and paste the config it shows into your agent:

```json
{"mcpServers": {"powerdeck": {"type": "http", "url": "http://<server's tailnet address>:47323/mcp",
  "headers": {"Authorization": "Bearer pdk_…"}}}}
```

Tools: `list_devices`, `get_status`, `power_on`, `power_off`, `cancel`. Whatever the agent asks, the helper enforces the limits: only the devices and permissions on its key, never while someone is using the device, always the 60-second countdown, at most one shutdown per device per 10 minutes, and never the device the agent itself runs on. The key works only from the first device that uses it. Every action appears in History and in a notification on your phone.

## What the helper sets up

| | Windows | Linux |
|---|---|---|
| Runs as | A service that starts before anyone signs in | A systemd service |
| Network | Firewall rule admitting only Tailscale addresses | Answers only Tailscale addresses and loopback |
| Wake-on-LAN | Armed on the wired card; Fast Startup off so "shut down" is a real power-off | Armed with ethtool at every start |
| Tailscale | Unattended mode, so it's reachable at the sign-in screen | |
| Updates | Automatic, Ed25519-signature-checked | Automatic, Ed25519-signature-checked |

Your BIOS needs Wake-on-LAN enabled. On many boards that's *ErP Ready: Disabled* and *Power On By PCI-E: Enabled*. For a server, also turn on *Restore on AC power loss* so it comes back after a power cut.

To remove the helper: on Windows, Settings → Apps → PowerDeck → Uninstall. On Linux, `curl -fsSL https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.sh | sudo sh -s -- --uninstall` (add `--purge` to delete its data too).

## Troubleshooting

| Problem | Try |
|---|---|
| A device shows **Off** but it's on | Check Tailscale is connected on both, and look at the device's setup checks |
| **Turn on** does nothing | The device must be wired (Wi-Fi can't wake), Wake-on-LAN must be on in the BIOS, and something on that network has to be on to send the signal: another helper, a NanoKVM, or your phone at home |
| Reachable only after someone signs in (Windows) | Fix the **Tailscale unattended** check |
| "Another Tailscale account" | The device belongs to a different account: enter the pairing code its helper shows |
| An always-on device drops off after a few months | In the Tailscale admin console, **Disable key expiry** for it |

Logs: Windows `C:\ProgramData\PowerDeck\logs\`, Linux `journalctl -u powerdeck`.

## Security

- **Trust comes from Tailscale.** A helper asks Tailscale who's calling. Only devices signed in to the same Tailscale account (not shared or tagged ones) are trusted automatically. Anyone else needs a one-time 8-character pairing code shown on the device (10 minutes, 5 wrong guesses and it's gone). Agents use keys that are limited in what they can do and bound to one device.
- **Phone ↔ helper** traffic is HTTPS pinned to that helper's own certificate, inside Tailscale's WireGuard tunnel. Helpers answer only tailnet addresses and throttle repeated failures.
- **The NanoKVM password** never leaves your phone. It's encrypted with a key in Android's hardware keystore, and the app is excluded from backups.
- **Downloads** are checked: installers and updates verify an Ed25519 signature, Android only accepts updates signed with the same key, and every release has SHA-256 checksums and a GitHub build attestation.

Details: [SECURITY.md](SECURITY.md) · protocol: [docs/protocol.md](docs/protocol.md)

### Checking a download

```
gh attestation verify PowerDeck-Setup.exe --repo ProjectZero02/powerdeck
```

## Building

| Part | Stack | Test |
|---|---|---|
| `helper/` | Go, no CGO; Windows installer with Inno Setup | `go test ./...` · `go run ./tools/demo` shows the Windows window with sample data |
| `android/` | Kotlin, Jetpack Compose (Material 3), Glance widget | `./gradlew testDebugUnitTest` (unit tests plus Roborazzi screenshots) |

CI ([`.github/workflows/build.yml`](.github/workflows/build.yml)) runs all tests. It also installs and uninstalls the real helper on Windows and Linux runners, including a real scheduled shutdown that is then cancelled. Pushing a tag `vX.Y.Z` publishes a signed release.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
