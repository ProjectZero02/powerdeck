# Changelog

## 2.0.0

PowerDeck now controls any number of computers.

- **Linux helper** (including Proxmox), next to Windows. One-line install on both (`install.sh`, `install.ps1`), signature-checked.
- **No pairing for your own devices:** a helper trusts phones signed in to the same Tailscale account. Devices on another account use an 8-character code.
- **Add devices in seconds:** scan one helper's QR code (or type its Tailscale name) and the others it finds on your tailnet, helpers and NanoKVMs, appear to add with one tap.
- **Wake from anything that's on:** any helper on the same network sends the Wake-on-LAN signal, or a NanoKVM, or the phone itself at home.
- **NanoKVM for any device:** link one to any computer's power button. It turns the computer on (after checking the power light) and is the fallback for shutting down. Force-off is hidden behind a confirmation.
- **Someone using it?** Shutdown and restart ask first when someone is active or a game is full-screen. A 60-second countdown you can cancel.
- **Hold to shut down** instead of confirmation dialogs.
- **Schedules:** shut down, restart or wake at set times, skipped while the computer is in use.
- **Setup checks with Fix** buttons, **History**, **Update and shut down** (Windows).
- **AI agents (MCP):** let an agent or script check and control devices through an always-on helper, with strict limits and per-key permissions.
- **Notifications** when a schedule or an agent acts, or a device goes offline.
- **Widget** with every device, **four Quick Settings tiles**, per-device home-screen shortcuts.
- **Safer by design:** a NanoKVM presses the power button only when the helper doesn't answer and the power light reads off, and linking one checks the light matches the computer. Keys from a pairing code control only that device. The PC window works only for the person at the screen (or an administrator).
- Coming from 1.0: install the 2.0 helper over the old one, then add the PC again in the app.

## 1.0.0

First release.

- **Windows:** one-click installer. A background service handles shutdown, restart and cancel for paired phones. The PC window shows pairing QR codes and paired phones and runs setup checks. Wake-on-LAN, Fast Startup and Tailscale unattended mode are set up automatically. It finds the NanoKVM and Proxmox on the tailnet. Updates are automatic and signature-checked.
- **Android:** QR pairing. A dashboard shows live PC and server status with power buttons (PC: on, shut down, restart, cancel; server: on, clean shutdown). The NanoKVM password is stored in the hardware keystore. Tailscale turns on automatically when needed. Adds a home-screen widget, Quick Settings tiles and in-app updates.
