# Changelog

## 2.2.3

- **PC window setup shows once.** The first-run setup was only marked done by **Done** on its last step, so closing the window part-way (the main screen has the same QR codes) started it over on every open. It's now marked done as soon as it's shown.

## 2.2.2

- **Phone app updates.** Tapping **Update** more than once (or on both the home screen and in Settings) started downloads that overwrote each other, and the update then failed with "The download is corrupt (checksum mismatch)". Only one download runs now, its button shows it's downloading, each download goes to a file of its own, and a damaged download is retried once. If your phone shows that error on the way to this version, tap **Update** once and wait, or install `PowerDeck.apk` from the release page.

## 2.2.1

- **PC window over Remote Desktop.** An administrator of the PC can now open the PowerDeck window over Remote Desktop without running it as administrator. Standard accounts in another session are still refused, and the message now says what to do.
- **Wake-on-LAN on Windows.** The check could report Wake-on-LAN as off while it was on, and **Fix** failed with a block of XML ("the driver reports magic-packet wake as <Objs…"): PowerShell's progress messages, such as "Preparing modules for first use", were being read as the answer. PowerDeck now reads only the result, and a real error shows PowerShell's own message. A failed `powercfg` step no longer stops the fix.

## 2.2.0

KVMs move to an always-on PowerDeck computer, which can now reset a frozen one.

**Update the helper on that computer and the phone app.** Helpers before 2.2 can't hold a KVM; the app then keeps driving it itself, as before.

- **KVM hub.** The app hands each NanoKVM to one always-on helper (by default your only Linux one, for example a home server; pick another, or keep them on the phone, in **Settings → KVM hub**). The phone and agents then go through it, and the phone falls back to the KVM itself when the hub can't be reached. The hub stores the NanoKVM password sealed with a key kept apart from its data (DPAPI on Windows).
- **Reset a frozen computer.** When a computer's helper stops answering, its page offers **Reset** (a press of the reset button), and **Force off** (a 12-second hold) stays in the menu. Both read the power light before and after, need it to be on, are refused while the computer still answers, and are for your own devices only. After a reset the hub checks that the computer answers again within 5 minutes; a reset button that had no effect is remembered. Through the API, `force-off` can also start the computer again after the hold.
- **Wiring is learned.** Nothing needs to be declared about how the KVM is connected: the hub learns whether the power light, the power button and the reset button are wired from what it sees, and the device page shows it, with **Check again**.
- **Shutting down without the helper:** a power press for a clean shutdown, owner only, and only while the light is known to read on.
- **API:** `/v2/kvms`, `/v2/machines` and `reset`/`force-off` in the relay (owner devices only); the `kvm` cap. See [docs/protocol.md](docs/protocol.md#kvms-on-the-hub) and [docs/nanokvm-hardening.md](docs/nanokvm-hardening.md).

## 2.1.0

Security groundwork before PowerDeck can reset a frozen computer through a KVM.

**Update the phone app first.** The 2.0 app can't pair with a 2.1 helper.

- **Owner devices.** A helper trusts the phones and computers that scanned its code, not every device on your Tailscale account. Upgraded helpers keep the old rule until the updated app claims them, which it does on its own. The code is now in every QR code. Owner devices are listed, and can be removed, on the device's page in the app, in the PC window and with `powerdeck-helper owner list|add|rm`.
- **Phones on another account** that pair with the code can turn that device on and off, but no longer change its settings, keys or updates.
- **Helpers reach each other with peer keys** (status, wake, power only) that the app sets up, instead of as you.
- **Listening:** the helper listens only on loopback and its Tailscale addresses, never on every network card, and follows Tailscale when it starts late or changes address.
- **Updates are staged:** a release installs on its own 3 days after it's published, and you can pause that for 7 days in the app or the PC window. The PC window has an automatic-updates switch. New Linux installs (servers) start with automatic updates off. SECURITY.md explains the signing-key risk.
- **App:** no plain HTTP except to Tailscale names. NanoKVMs are linked by their Tailscale name; ones linked by IP address are renamed on their own. MagicDNS must be on. A NanoKVM that refuses an action now says so, and long presses wait long enough.

## 2.0.4

- **Settings → Updates** now shows what happened. Checking the app for an update says "Checking…", then "Up to date" or "PowerDeck x.y.z is ready" with an **Update** button. Before, the result only appeared after going back to the home screen. **Update devices now** and the automatic-updates switch also report their result on the Settings screen.

## 2.0.3

A proper Windows window, a scanner that works everywhere, and simpler controls.

- **Windows:** PowerDeck opens in its own window instead of a browser tab, with a title bar and scroll bar that match the light or dark theme. The first start walks through three steps you can skip: a code to get the app, a code to add the PC, and its setup checks one by one, with optional AI agent setup. **Show icon in hidden icons** adds a taskbar icon that opens PowerDeck. The PC's address, network card and other devices show straight away.
- **Scanner:** the QR scanner is now built into the app instead of relying on Google Play services, which could close the app without opening the camera. It asks for the camera once, accepts only PowerDeck codes, and has a light button.
- **Add devices:** a radar while PowerDeck searches your tailnet, **Search again** when nothing new turns up, and the scan button at the bottom. Type a **Tailscale name or IP**. Install commands moved to GitHub behind an **Install** link.
- **Device cards** have a **⋮** menu: Rename, Settings, Add to home screen, Delete. **Delete** asks whether to remove the device from this phone only or also uninstall PowerDeck from it.
- **AI agents:** one switch per device, **Allow AI agents to use this device**. Off, agents can still see whether it's on but can't turn it on or off, on that device or through a hub. Until an agent is connected the switch is a **Set up** button that explains how.
- **Add sheet:** scanning a code opens **Add <computer>**, where you can rename it before adding. The first time, it also asks how the buttons behave.
- **Button settings** (in Settings, and in the first Add sheet): **Hold for 3 s to activate** for Shut down and Restart, and **Countdown to action** in seconds (0–600, default 60). Shortcuts, tiles and the widget use the same countdown.
- **Setup checks** on a device page fold away, and stay folded once everything passes.

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
