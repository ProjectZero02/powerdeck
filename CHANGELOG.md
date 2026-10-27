# Changelog

## 1.0.0

First release.

- **Windows:** one-click installer. A background service handles shutdown, restart and cancel for paired phones. The PC window shows pairing QR codes and paired phones and runs setup checks. Wake-on-LAN, Fast Startup and Tailscale unattended mode are set up automatically. It finds the NanoKVM and Proxmox on the tailnet. Updates are automatic and signature-checked.
- **Android:** QR pairing. A dashboard shows live PC and server status with power buttons (PC: on, shut down, restart, cancel; server: on, clean shutdown). The NanoKVM password is stored in the hardware keystore. Tailscale turns on automatically when needed. Adds a home-screen widget, Quick Settings tiles and in-app updates.
