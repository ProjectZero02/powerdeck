<p align="center">
  <img src="docs/images/banner.png" alt="PowerDeck: turn your computers on and off from your phone, from anywhere">
</p>

<p align="center">
  <a href="https://github.com/ProjectZero02/powerdeck/releases/latest"><img src="https://img.shields.io/github/v/release/ProjectZero02/powerdeck?label=release&color=5B4CF0" alt="Latest release"></a>
  <a href="https://github.com/ProjectZero02/powerdeck/releases"><img src="https://img.shields.io/github/downloads/ProjectZero02/powerdeck/total?color=5B4CF0" alt="Downloads"></a>
  <a href="https://github.com/ProjectZero02/powerdeck/actions/workflows/build.yml"><img src="https://github.com/ProjectZero02/powerdeck/actions/workflows/build.yml/badge.svg" alt="Build"></a>
  <img src="https://img.shields.io/badge/platforms-Android%20%C2%B7%20Windows%20%C2%B7%20Linux-5B4CF0" alt="Android, Windows, Linux">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/ProjectZero02/powerdeck?color=5B4CF0" alt="MIT license"></a>
</p>

<p align="center">
  <b><a href="#set-up-in-3-steps">Set up</a></b> ·
  <a href="#features">Features</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#safe-by-design">Safety</a> ·
  <a href="#ai-agents-mcp">AI agents</a> ·
  <a href="#troubleshooting">Troubleshooting</a>
</p>

---

**PowerDeck** turns your PCs and servers on, off and restarts them from an Android phone, at home or anywhere else. It's free, open source, and needs no port forwarding, cloud account or subscription. Everything travels over your own [Tailscale](https://tailscale.com) network.

- **One command per computer.** Windows and Linux (including Proxmox) set themselves up: service, firewall, Wake-on-LAN, Fast Startup.
- **Finds everything itself.** Add one computer, and the others, plus any NanoKVM, show up ready to add.
- **Careful by default.** Shutdowns count down for 60 seconds and can be cancelled. PowerDeck asks first if someone's using the computer, and it never presses a power button blind.

## Set up in 3 steps

> [!IMPORTANT]
> You need **[Tailscale](https://tailscale.com/download)** (free for personal use) on your phone and on every computer, **signed in to the same account**. That's how your phone reaches your computers from anywhere, and how PowerDeck knows they're yours.

**1. Install the helper on each computer**

| | Paste this | |
|---|---|---|
| **Windows** | `irm https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.ps1 \| iex` | in **PowerShell** |
| **Linux / Proxmox** | `curl -fsSL https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.sh \| sh` | in a **terminal** (as root, or any user with sudo) |

Both installers check the download against the release's checksums (on Linux, its Ed25519 signature too), set everything up and show a **QR code**. On Windows it opens in a window; on Linux it's printed in the terminal (`powerdeck-helper qr` shows it again). They also tell you about anything they can't fix themselves, such as a BIOS setting. Prefer a regular installer on Windows? Download [PowerDeck-Setup.exe](https://github.com/ProjectZero02/powerdeck/releases/latest/download/PowerDeck-Setup.exe).

**2. Install the app on your phone**

Download [**PowerDeck.apk**](https://github.com/ProjectZero02/powerdeck/releases/latest/download/PowerDeck.apk) on the phone (the Windows window has a QR code for it) and open it. Android asks once to allow installs from your browser.

**3. Scan the code**

Tap **Scan the helper's code**. Your other computers and NanoKVMs then appear under **Found on your tailnet**: tap **Add** on each one. A NanoKVM asks for its password and which computer's power button it's wired to.

> [!NOTE]
> A NanoKVM shows up only once it's on Tailscale too: turn Tailscale on in its web settings and sign it in to the same account. Keep its web page on plain HTTP (the default); Tailscale already encrypts the connection.

That's it. Try **Turn on** once while the computer is off to confirm Wake-on-LAN works.

<details>
<summary><b>Windows says "Windows protected your PC"</b></summary>

The installer isn't code-signed yet, so SmartScreen warns about it. Choose **More info → Run anyway**. Every release is built by GitHub Actions straight from this repository and comes with checksums, Ed25519 signatures and a [build attestation](#checking-a-download).
</details>

<details>
<summary><b>Always-on computers: turn off Tailscale key expiry</b></summary>

Tailscale signs devices out every 180 days by default. For a server, or anything PowerDeck wakes up, open the [Tailscale admin console](https://login.tailscale.com/admin/machines), click **⋯** → **Disable key expiry** on that machine.
</details>

## Screenshots

<table>
  <tr>
    <td align="center" width="25%"><img src="docs/images/home.png" alt="Home: every device with its state and power buttons"><br><sub><b>Every device at a glance</b></sub></td>
    <td align="center" width="25%"><img src="docs/images/device.png" alt="Device page: setup checks with Fix buttons, schedules"><br><sub><b>Setup checks, schedules, history</b></sub></td>
    <td align="center" width="25%"><img src="docs/images/add-found.png" alt="Adding devices found on the tailnet"><br><sub><b>Found on your tailnet</b></sub></td>
    <td align="center" width="25%"><img src="docs/images/home-dark.png" alt="Dark mode"><br><sub><b>Dark mode and Material You</b></sub></td>
  </tr>
</table>

## Features

| | |
|---|---|
| **On · Off · Restart** | Clean shutdowns with a 60-second countdown you can cancel. On Windows, **Update and shut down** when updates are waiting. |
| **Turn on from anywhere** | Wake-on-LAN, sent by any PowerDeck computer on the same home network (an always-on server is ideal), or by a NanoKVM, or by your phone when it's at home. |
| **NanoKVM** | Wire a [NanoKVM](https://github.com/sipeed/NanoKVM) to any computer's power button to turn it on even where Wake-on-LAN can't, and as a fallback for shutting down. Nothing is installed on the KVM. |
| **Someone using it?** | Shutdowns and restarts ask first when someone is active or a game is full-screen, and say who. |
| **Proxmox aware** | Shows running VMs. A shutdown stops them cleanly first. |
| **Schedules** | Shut down, restart or wake at set times on chosen days, skipped while the computer is in use. |
| **Setup checks** | Each device reports what's wrong, with a **Fix** button where it can fix it remotely (Wake-on-LAN, Fast Startup, firewall, Tailscale unattended mode). |
| **Shortcuts** | A home-screen widget with every device, four Quick Settings tiles, home-screen shortcuts per device, and long-press shortcuts on the app icon. |
| **Notifications** | When a schedule or an agent acts, or a device goes offline. |
| **History** | Who did what and when, on every device. |
| **AI agents** | Let an agent or a script check and control your devices over [MCP](https://modelcontextprotocol.io), within limits you set. |
| **Other people's computers** | A device on another Tailscale account can be added with a one-time 8-character pairing code. |

## How it works

```mermaid
flowchart LR
    phone["PowerDeck app<br/>(Android)"]
    pc["Windows PC<br/>helper"]
    srv["Linux / Proxmox<br/>helper"]
    kvm["NanoKVM"]
    agent["AI agent<br/>(optional)"]

    phone -- "HTTPS over Tailscale<br/>(pinned certificate)" --> pc
    phone -- "HTTPS over Tailscale" --> srv
    phone -. "its web API<br/>(password stays on the phone)" .-> kvm
    srv -- "Wake-on-LAN<br/>(home network)" --> pc
    kvm -- "power button + LED" --> pc
    agent -. "MCP, limited key" .-> srv
```

**The helper** is a small Go service (about 10 MB) on each computer. It answers only Tailscale addresses. It shuts its own computer down or restarts it, sends Wake-on-LAN to computers on its network, and finds the other PowerDeck devices on your tailnet. **The app** talks to each helper directly; there's no server in between. A computer that's off can't answer, so the app asks something that's still on (another helper, a NanoKVM, or the phone itself) to wake it.

| What the helper sets up | Windows | Linux |
|---|---|---|
| Runs as | A service that starts before anyone signs in | A systemd service |
| Network | A firewall rule admitting only Tailscale addresses | Answers only Tailscale addresses and loopback |
| Wake-on-LAN | Armed on the wired network card; Fast Startup off, so "shut down" is a real power-off | Armed with `ethtool` at every start |
| Tailscale | Unattended mode, so it's reachable at the sign-in screen | |
| Updates | Automatic, Ed25519-signature-checked | Automatic, Ed25519-signature-checked |

Your BIOS also needs Wake-on-LAN enabled. On many boards that's *ErP Ready: Disabled* and *Power On By PCI-E: Enabled*. For a server, also turn on *Restore on AC power loss* so it comes back after a power cut.

## Safe by design

- **Only you.** A helper asks Tailscale who's calling. It trusts only devices signed in to your Tailscale account (not shared or tagged ones). Anyone else needs a one-time pairing code shown on the device, which lasts 10 minutes and dies after 5 wrong guesses. A device added that way can control only that device (and send Wake-on-LAN); it can't reach your others or create keys.
- **No accidental power-offs.** A KVM press toggles power, so PowerDeck presses to turn on only when the computer's helper doesn't answer *and* the power light reads off. It won't press at all until it has seen that computer's light on, which proves the LED cable is connected (an unwired LED always reads off). Linking a KVM checks that its light matches the computer. Holding the button down to force a computer off is hidden behind a confirmation.
- **Nothing exposed.** No ports are opened to the internet. Phone-to-helper traffic is HTTPS pinned to that helper's own certificate, inside Tailscale's WireGuard tunnel.
- **Your NanoKVM password stays on your phone,** encrypted with a key in Android's hardware keystore. The app is excluded from backups.
- **Checked downloads.** Automatic updates and the Linux installer verify an Ed25519 signature; the Windows one-liner checks the release's SHA-256. Android accepts updates only when they're signed with the same key. Every release has SHA-256 checksums and a GitHub build attestation.

Details: [SECURITY.md](SECURITY.md) · protocol: [docs/protocol.md](docs/protocol.md)

## AI agents (MCP)

Let an AI agent or a script check and control your devices through [MCP](https://modelcontextprotocol.io). In the app, open an always-on device (a home server is ideal) and tap **Connect an agent**. Choose which devices it may use and whether it may shut them down, then paste the config it shows into your agent:

```json
{"mcpServers": {"powerdeck": {"type": "http", "url": "http://<server's tailnet address>:47323/mcp",
  "headers": {"Authorization": "Bearer pdk_…"}}}}
```

Tools: `list_devices`, `get_status`, `power_on`, `power_off`, `cancel`. Whatever the agent asks for, the helper enforces these limits:
- only the devices and permissions on its key;
- never while someone is using the computer, and always with the 60-second countdown;
- at most one shutdown per device per 10 minutes;
- never the computer the agent itself runs on.

The key works only from the first device that uses it. Every action appears in History and in a notification on your phone.

## Troubleshooting

<details>
<summary><b>A device shows Off, but it's on</b></summary>

Check that Tailscale is connected on both the phone and the computer, then look at the device's setup checks in the app. Logs: Windows `C:\ProgramData\PowerDeck\logs\`, Linux `journalctl -u powerdeck`.
</details>

<details>
<summary><b>Turn on does nothing</b></summary>

- The computer must be connected by **cable**; Wi-Fi can't wake a computer that's off.
- Wake-on-LAN must be enabled in the **BIOS** (see above), and the **Wake-on-LAN** and **Fast Startup** setup checks must be green.
- Something on that home network has to be on to send the signal: another PowerDeck computer, a NanoKVM, or your phone while it's on home Wi-Fi.
</details>

<details>
<summary><b>Windows is reachable only after someone signs in</b></summary>

Fix the **Tailscale unattended** setup check (one tap on **Fix**).
</details>

<details>
<summary><b>"This device belongs to another Tailscale account"</b></summary>

The phone and the computer are signed in to different Tailscale accounts. Either sign them in to the same one, or enter the pairing code the computer shows: on Windows it's in the PowerDeck window, on Linux run `powerdeck-helper qr`.
</details>

<details>
<summary><b>An always-on device drops off after a few months</b></summary>

That's Tailscale key expiry. In the Tailscale admin console, **Disable key expiry** for it.
</details>

<details>
<summary><b>Uninstalling</b></summary>

- **Windows:** Settings → Apps → PowerDeck → Uninstall.
- **Linux:** `curl -fsSL https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.sh | sh -s -- --uninstall` (add `--purge` to delete its data too).
- **Phone:** uninstall the app as usual.
</details>

### Checking a download

Each release lists SHA-256 checksums in `SHA256SUMS.txt`, and GitHub can confirm a file was built here:

```sh
gh attestation verify PowerDeck-Setup.exe --repo ProjectZero02/powerdeck
```

## Building from source

| Part | Stack | Test |
|---|---|---|
| [`helper/`](helper) | Go, no CGO; Windows installer with Inno Setup | `go test ./...` · `go run ./tools/demo` shows the Windows window with sample data |
| [`android/`](android) | Kotlin, Jetpack Compose (Material 3), Glance widget | `./gradlew testDebugUnitTest`: unit tests plus Roborazzi screenshots (the images on this page) |

CI ([`build.yml`](.github/workflows/build.yml)) runs every test. It also installs and uninstalls the real helper on Windows and Linux runners, including a real scheduled shutdown that's then cancelled. Pushing a `vX.Y.Z` tag publishes a signed release.

## Contributing

Bug reports, ideas and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md), and report security issues privately as described in [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE) © 2026 ProjectZero02
