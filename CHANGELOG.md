# Changelog

## 2.0.2

Fixes from a final review.

- **NanoKVM safety:** a helper that answers with any error (throttled, a lost key) now counts as on, so its power button is never pressed. The check needs no key. A confirmed "press the power button" re-checks the helper first and uses it if it's back, so the in-use check still applies.
- **Windows:** if an update's setup fails part-way, the service is started again, so the PC stays reachable.
- **Quick Settings tile** shows "Tap to check" instead of "Off" when a device has a problem.
- **Clearer errors:** a failure to save a key is reported as such, not as "too many keys". Devices on another account are no longer called "older helpers".
- **Linux installer** passes the version through sudo in a way restricted sudo rules accept.
- **Banner and README:** logo beside the title, diagram centered.

## 2.0.1

Fixes from a final security and reliability review, and updates you control from the phone.

- **Cleaner design:** one top bar style on every screen, 16 dp margins, the same type scale throughout. A device's dot says on or off by itself; text appears only for news (in use, a countdown, a problem). A ring means unknown. Passed setup checks fold into one row, the power-button section shows only when there's a KVM, and wording is shorter everywhere.
- **Updates from the app:** Settings → **Update devices now** updates every computer at once, and **Update devices automatically** turns self-updates on or off everywhere. A device page shows when its update is ready, with an **Update** button. The app notifies you once when a new version of itself is out.
- **Security:** a program on a computer running the helper could act as its owner by calling the computer's own Tailscale address. It now needs the local token, like any other local program. A key from a pairing code can no longer read new pairing codes or change system settings.
- **Easier Linux install:** the one-liner is now `curl … | sh`. It works in a root shell (Proxmox has no `sudo`) and as any user with sudo.
- **Reliability:** settings are saved durably with a backup copy, so a power cut can't leave the helper unable to start. A deliberately deleted settings file stays deleted (only a damaged one is restored from the backup). A schedule can't run twice if the helper restarts in the same minute. An update waits until no shutdown is counting down, and no new countdown can start while it restarts the helper. An update that never finishes is reported after 10 minutes instead of showing "Updating" forever.
- **Proxmox:** the list of running VMs is cached for 15 seconds instead of being read on every status check.
- **Schedules** say when the computer's clock is in a different time zone from your phone's.
- **NanoKVM:** a computer whose helper answers with a changed identity counts as on, so its button is never pressed.
- **Windows:** at install, a data folder that another user created (or a link to somewhere else) is moved aside and replaced, and anything inside it that can't be taken over is removed.
- **Linux installer:** when run as a normal user it downloads itself again for sudo with curl or wget, and stops with a clear message if that download fails.
- **App messages:** updating every device says what went wrong on each one (a countdown, no connection), and names any device with an older helper that will update itself.

## 2.0.0

PowerDeck now controls any number of computers.

- **Linux helper** (including Proxmox), next to Windows. One-line install on both (`install.sh`, `install.ps1`), checked against the release's checksums (and on Linux its Ed25519 signature).
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
- **Safer by design:** a NanoKVM presses the power button only when the helper doesn't answer and the power light reads off, and only once it has seen that computer's light on; linking one checks the light matches the computer. Keys from a pairing code control only that device and can't create other keys. The PC window works only for the person at the screen (or an administrator).
- **Coming from 1.0:** the 1.0 helper updates itself to 2.0, after which the 1.0 app can't reach it. Install the 2.0 app, then add the PC again (scan its code).

## 1.0.0

First release.

- **Windows:** one-click installer. A background service handles shutdown, restart and cancel for paired phones. The PC window shows pairing QR codes and paired phones and runs setup checks. Wake-on-LAN, Fast Startup and Tailscale unattended mode are set up automatically. It finds the NanoKVM and Proxmox on the tailnet. Updates are automatic and signature-checked.
- **Android:** QR pairing. A dashboard shows live PC and server status with power buttons (PC: on, shut down, restart, cancel; server: on, clean shutdown). The NanoKVM password is stored in the hardware keystore. Tailscale turns on automatically when needed. Adds a home-screen widget, Quick Settings tiles and in-app updates.
