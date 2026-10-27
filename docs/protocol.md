# PowerDeck protocol (v1)

## Pairing QR code

The PC window shows a link like this one (shown here wrapped):

```
powerdeck://pair?v=1&n=Gaming-PC&h=100.101.1.10&p=47321&f=<pin>&c=<code>
                 &m=d8:5e:d3:12:34:56&k=100.101.1.20&x=100.101.1.30:8006
```

| Key | Meaning |
|---|---|
| `v` | Protocol version (`1`) |
| `n` | PC name, for display |
| `h`, `p` | PC's Tailscale IPv4 address and API port |
| `f` | base64url SHA-256 of the PC's TLS certificate (DER). The phone pins this |
| `c` | One-time pairing code: 26 lowercase base32 characters (128 bits). Valid for 10 minutes, consumed on use, discarded after 5 wrong guesses |
| `m` | MAC address of the PC's wired network card (for Wake-on-LAN). Optional |
| `k` | NanoKVM address found on the tailnet. Optional |
| `x` | Proxmox `host:port` found on the tailnet. Optional |

The PC finds `k` and `x` by asking `tailscale status` for online Linux peers. It then looks for the NanoKVM page title on port 80 and the Proxmox page on port 8006.

## Phone → PC API

The API is HTTPS on port 47321, TLS 1.3 only. The phone accepts only the pinned certificate.
The PC refuses any source address outside `100.64.0.0/10`, `fd7a:115c:a1e0::/48` and loopback.
After 10 failed attempts (bad token or bad pairing code) from one address within 10 minutes, that address is blocked for 10 minutes.

| Method | Path | Auth | Body → Response |
|---|---|---|---|
| GET | `/v1/info` | none | → `{app, name, version, apiVersion}` |
| POST | `/v1/pair` | pairing code | `{code, deviceName, deviceModel}` → `{deviceId, token, pc: <status>}` |
| GET | `/v1/status` | token | → `{name, version, apiVersion, uptimeSeconds, bootTime, pending, mac, nanokvm?, proxmox?}` |
| POST | `/v1/power/shutdown` | token | `{delaySeconds?}` (default 30, max 600) → `202 {pending}` or `409` |
| POST | `/v1/power/restart` | token | same as shutdown |
| POST | `/v1/power/cancel` | token | → `200` or `409` (nothing pending) |
| DELETE | `/v1/device` | token | → `204`. The phone unpairs itself |

`token` is sent as `Authorization: Bearer <token>`: 32 random bytes, base64url. The PC stores only its SHA-256.
Errors look like `{"error": "<code>", "message": "<human text>"}`.

## Phone → NanoKVM

The phone uses the NanoKVM's own web API (firmware 2.x), over Tailscale:

- `POST /api/auth/login` with `{username, password}`. The password uses the NanoKVM UI's encoding: `encodeURIComponent(CryptoJS.AES.encrypt(pw, "nanokvm-sipeed-2024"))`. The token comes back in `data.token` or in the `nano-kvm-token` cookie.
- `GET /api/vm/gpio` returns `data.pwr`, the state of the server's power LED.
- `POST /api/vm/gpio` with `{type: "power", duration: 800}` presses the power button briefly.
- `POST /api/network/wol` with `{mac}` sends a magic packet from the NanoKVM's LAN.

## Updates

GitHub's `releases/latest` is the source of truth.

- **Windows** downloads `PowerDeck-Setup.exe` and `PowerDeck-Setup.exe.sig`, then checks the base64 Ed25519 signature against the public key built into the app. Only then does it run the installer with `/VERYSILENT`.
- **Android** downloads `PowerDeck.apk` and checks its SHA-256 against the release asset digest. It then installs through `PackageInstaller`; Android enforces the signing key.
