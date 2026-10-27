# PowerDeck protocol (v2)

PowerDeck has three kinds of participants, all on the same [Tailscale](https://tailscale.com) network:

| Participant | Role |
|---|---|
| **Helper** (`powerdeck-helper`, Windows or Linux) | Shuts down / restarts its own machine, reports status and setup checks, sends Wake-on-LAN to its LAN, finds other PowerDeck devices on the tailnet |
| **Phone app** | Lists devices, sends commands, picks a waker for each device |
| **KVM** (NanoKVM) | Presses a computer's power button and reads its power LED. Used through its own web API; nothing is installed on it. (PiKVM is recognised by discovery, but the app doesn't drive it yet.) |

## Identity and trust

Helpers listen on TCP **47321** (HTTPS, TLS 1.3, self-signed certificate) and answer only tailnet
source addresses (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) and loopback.

On every request the helper asks its local Tailscale who the caller is (`tailscale whois`, cached
for 60 s per address). A caller is one of:

| Caller | How it's recognised | Allowed |
|---|---|---|
| **Owner** | Untagged node owned by the same Tailscale user as this helper's node, not shared in from another tailnet, and not this node itself (a program here reaching its own tailnet address is local, like loopback: it needs the local token) | Everything |
| **Key holder** | Any tailnet caller presenting `Authorization: Bearer <key>` for a key this helper issued | The key's scopes |
| **Anyone else** | | `GET /v2/hello` only |

Keys are created in two ways:

- **Pairing code** (for phones on another Tailscale account, or when the helper's node is tagged so
  it has no owner): the helper shows an 8-character code (`XXXX-XXXX`, Crockford base32, 40 bits,
  single use, 10 minutes, 5 wrong guesses and it's gone). `POST /v2/pair` exchanges it for a key
  with all scopes, for **this device only**: it can't relay to other devices, use `/mcp`, manage
  keys, or add wake schedules. It may use the raw-MAC `/v2/wake` (a tagged hub has no owner, and a
  magic packet only turns things on).
- **Automation keys** (for AI agents and scripts): created by an owner, either from the phone app
  (`POST /v2/keys`) or on the device (`powerdeck-helper key add NAME --scopes …`).

Key scopes: `status`, `wake`, `power` (shutdown/restart/cancel), `force` (shut down even while someone
is using the machine), `admin` (keys, settings, fixes). A pairing-code key gets all of them; an
automation key gets what it was created with (default `status,wake`) and can't hold `force` or
`admin`. The helper stores only the SHA-256 of each key. Keys are 32 random bytes, base64url,
prefixed `pdk_`.

A key with a device list is held to it on every route, including this device's own
`/v2/power` (`403 device`), and can't use the raw-MAC `/v2/wake` (it wakes by name through the
relay). If Tailscale can't identify a caller because it isn't answering, the helper replies
`503 tailscale` (retry) rather than treating the caller as a stranger.

An automation key is **bound to the tailnet node that first uses it**: from then on the helper
accepts it only from that node (checked with `whois`), so a leaked key is useless elsewhere.
It can also be limited to named devices (see [Agents](#agents-mcp)).

Ten failed attempts (bad key or code) from one address within 10 minutes block that address for 10 minutes.

### Certificates

The phone pins each helper's certificate (base64url SHA-256 of the DER) the first time it adds it:
from the QR code, from the introducing helper's peer list, or on first contact when the user typed
the device's name. Tailscale already authenticates the address (WireGuard), so pinning on first
contact is safe; the pin then stops a later look-alike.

## Adding devices

The phone can't list tailnet devices itself (Android apps can't read Tailscale's peer list). It
meets **one** helper, then asks it for the rest:

1. **Introduction**: scan the QR code a helper shows (Windows window, or printed by the Linux
   installer / `powerdeck-helper qr`), or type the device's Tailscale name (MagicDNS), e.g. `workstation`.
   QR link: `powerdeck://add?v=2&n=<name>&h=<tailnet IPv4>&p=47321&f=<cert pin>[&c=<pairing code>]`.
2. **Find**: `GET /v2/peers` on that helper returns every PowerDeck helper and KVM it found on the
   tailnet. The phone offers them as "Found on your tailnet" and the user ticks the ones to add.
3. **Per device**: the phone calls `GET /v2/status` on each new helper (owner trust needs no
   pairing). For a KVM it asks for the KVM's password and which device's power button it's wired to.

## Helper API

All bodies are JSON. Errors: `{"error": "<code>", "message": "<human text>"}`.

| Method | Path | Scope | Body → Response |
|---|---|---|---|
| GET | `/v2/hello` | none | → `{app:"PowerDeck", apiVersion:2, version, name, os}` |
| GET | `/v2/status` | `status` | → [Status](#status) |
| GET | `/v2/peers` | `status` | → `{checkedAt, peers:[Peer]}`; `?refresh=1` rescans first (≤ 10 s) |
| POST | `/v2/power/shutdown` | `power` | `{delaySeconds?, force?, installUpdates?}` → `202 {pending}`, `409 in_use {presence}`, `409 pending {pending}`, `409 updating` while an update restarts the helper |
| POST | `/v2/power/restart` | `power` | same as shutdown |
| POST | `/v2/power/cancel` | `power` | → `200 {pending:null}` or `409 nothing_pending` |
| POST | `/v2/wake` | `wake` | `{mac}` → `200 {sentOn:[interface names]}`. Sends a magic packet to the helper's LANs |
| POST | `/v2/checks/{id}/fix` | owner | → `200 {check}` after applying the fix |
| GET | `/v2/schedules` | `status` | → `{schedules:[Schedule]}` |
| POST | `/v2/schedules` | `admin` | `Schedule` without `id` → `{id}`. Wake schedules (another device) are owner-only |
| DELETE | `/v2/schedules/{id}` | `admin` | → `204` |
| GET | `/v2/events` | `status` | → `{events:[{at, who, action, result}]}` (last 200) |
| GET | `/v2/keys` | owner | → `{keys:[{id, name, scopes, createdAt, lastUsed}]}` |
| POST | `/v2/keys` | owner | `{name, scopes, devices?, allowSelf?}` → `{id, key}` (shown once) |
| DELETE | `/v2/keys/{id}` | owner | → `204` |
| GET | `/v2/update` | owner | → `{current, latest, available, checkedAt, error, installing, auto}`; `?check=1` asks GitHub first |
| POST | `/v2/update` | owner | Installs the latest signed release now → `202 {…, installing: true}`, or `200` when up to date; `409 pending` during a shutdown countdown, `502 update_check` when GitHub can't be reached. The helper restarts a few seconds later; `installing` clears itself after 10 minutes if it doesn't, with `error` saying so |
| POST | `/v2/settings` | owner | `{autoUpdate?, agents?}` → `204`. `autoUpdate` (default on): check every 6 hours, install when nothing is counting down. `agents` (default on): `false` refuses `wake`, `shutdown` and `restart` from automation keys for this device, here and through any hub (`403 agents_off`); status and cancel still work |
| POST | `/v2/uninstall` | owner | `{confirm: <this device's name>}` → `202`, then the helper uninstalls itself and deletes its keys and settings. `409 pending` during a countdown. Offered when `caps` has `uninstall` |
| GET | `/v2/pairing` | owner | → `{link, code, expires}`: the add link and the current pairing code (to let someone on another account in) |
| POST | `/v2/pair` | pairing code | `{code, deviceName}` → `{key, status}` |
| DELETE | `/v2/key` | key holder | → `204`; revokes the caller's own key |
| POST | `/v2/devices/{name}/{action}` | per action | Relay: the helper performs `action` (`status`, `wake`, `shutdown`, `restart`, `cancel`) on another PowerDeck device it knows, see [Agents](#agents-mcp) |
| POST | `/mcp` | owner or automation key | [MCP](https://modelcontextprotocol.io) endpoint for AI agents |

`delaySeconds` defaults to 60 (0–600). Without `force`, shutdown/restart is refused with
`409 in_use` when someone is actively using the machine (input in the last 5 minutes, or a
full-screen app on Windows; an active SSH/console session on Linux). The phone shows who and asks
before retrying with `force: true`. Callers without the `force` scope can't override it.

### Status

```json
{
  "name": "Workstation", "os": "windows", "version": "2.0.0", "apiVersion": 2,
  "bootTime": "2026-10-27T06:08:02Z", "uptimeSeconds": 4123, "utcOffsetMinutes": 60,
  "update": {"available": true, "latest": "2.0.2", "installing": false, "error": "", "auto": true},
  "pending": {"action": "shutdown", "at": "2026-10-27T09:10:00Z", "by": "Pixel 9 Pro"},
  "presence": {"active": true, "user": "Alex", "idleSeconds": 40, "fullscreen": true},
  "guests": {"running": 3, "names": ["public", "private", "agent"]},
  "network": {"mac": "02:00:5e:00:00:01", "lan": ["192.168.1.0/24"], "wired": true},
  "caps": ["shutdown", "restart", "wake", "uninstall"],
  "checks": [Check],
  "agents": {"allowed": true, "keys": [{"name": "Atlas", "devices": ["Workstation", "nas"]}]},
  "you": {"kind": "owner", "name": "Pixel 9 Pro", "scopes": ["status", "wake", "power", "force", "admin"]}
}
```

`guests` appears on Proxmox hosts. `agents` and the `uninstall` cap are shown to owners only; `agents.keys` lists the automation keys this helper holds and the devices each may use. `updatesPending: true` (Windows) means "Update and shut down" is available. `presence` is omitted when the helper can't tell.

### Check

```json
{"id": "fast_startup", "level": "ok|warn|error|info", "title": "Fast Startup off",
 "detail": "Needed so a shutdown is a real power-off the network card can wake from.", "fixable": true}
```

| id | Platform | Meaning |
|---|---|---|
| `tailscale` | all | Tailscale running and signed in |
| `key_expiry` | all | This device's Tailscale key never expires (warns with the date otherwise; fixed in the Tailscale admin console) |
| `unattended` | Windows | Tailscale "Run unattended" on, so it's reachable at the sign-in screen (fixable) |
| `firewall` | Windows | Firewall rule for the helper, tailnet only (fixable) |
| `wol_nic` | all | Wake-on-LAN armed on the wired card (fixable; on Linux re-applied at every start) |
| `wired` | all | A wired card exists (Wi-Fi can't wake from shutdown) |
| `fast_startup` | Windows | Fast Startup off (fixable). PowerDeck's own shutdowns are always full shutdowns; this affects the Start menu's |
| `bios` | all | Reminder: BIOS Wake-on-LAN on, ErP/EuP off (`info`; confirmed by a successful test wake) |
| `ac_restore` | Linux | Reminder: BIOS "Restore on AC power loss" on, so a server comes back after a power cut (`info`) |
| `battery` | all | On battery / laptop: wake is unlikely (`warn`) |
| `updates` | all | Automatic updates on |

### Peer

```json
{"kind": "helper|nanokvm|pikvm", "name": "pve", "dnsName": "pve.example.ts.net",
 "address": "100.101.1.30", "port": 47321, "os": "linux", "online": true,
 "pin": "<cert pin, helpers only>", "version": "2.0.0"}
```

## Power paths

Every device can have up to three ways to be controlled, and the phone (or a hub) uses the safest one available:

| Path | On | Off / restart | Status |
|---|---|---|---|
| **Helper** on the device | | Clean shutdown/restart (primary) | Primary |
| **KVM** wired to the device's power button and LED (any number of devices, one KVM each) | Short press, **only while the LED says off** (primary when present) | Short press = the OS's own clean shutdown (fallback when the helper doesn't answer); 12 s press = force off (hidden, emergency) | Power LED (fallback) |
| **Wake-on-LAN** sent by a waker on the device's LAN | Magic packet (primary without a KVM, fallback with one) | | |

A KVM is linked to a device when it's added ("Which device's power button is this NanoKVM wired
to?"). The LED check is repeated just before every press, so a press can never turn a running
machine off by mistake.

### Wakers

Tailscale can't carry Wake-on-LAN broadcasts, so something already on, on the same LAN, sends them.
For each device the phone remembers its MAC and LAN subnets (from its last status) and tries, in order:

1. a helper whose `network.lan` overlaps the device's (`POST /v2/wake`);
2. a KVM on that LAN (NanoKVM `POST /api/network/wol`);
3. the phone itself, if it's on that LAN (UDP broadcast).

The magic packet is sent 3 times, to the subnet broadcast and to 255.255.255.255. Afterwards the
phone polls until the device's helper (or the KVM's LED) says it's on, and reports how long it took.

## Schedules

```json
{"id": "a1b2", "action": "shutdown|restart|wake", "target": "Workstation", "time": "01:00",
 "days": ["mon","tue","wed","thu","fri","sat","sun"], "skipIfInUse": true, "enabled": true}
```

`time` is local time on the helper that stores the schedule. Shutdown and restart schedules live on
the device's own helper (`target` is that helper). Wake schedules live on a helper that's on the
target's LAN and powered on at that time (typically a server); `target` names a known peer.
`skipIfInUse` (default on) skips a shutdown/restart when someone is using the device, and the skip
is logged. Each run is an event in the log.

## Notifications

The phone checks each device's `/v2/events` in the background (Android allows about every 15
minutes) and notifies about things the user didn't do from this phone: an agent or schedule acting,
a device going offline unexpectedly. Actions started on the phone show live progress instead.

## Agents (MCP)

AI agents and scripts talk to **one always-on helper** (a "hub", e.g. a home server). The hub
relays to the other devices, because a PC that's off can't answer for itself: it wakes devices
through its own LAN and forwards shutdown/restart/status to their helpers. The hub reaches other
helpers as their owner (same Tailscale user), so this only works between devices of one owner.

The hub serves MCP (streamable HTTP, stateless JSON-RPC `tools/list` and `tools/call`) with
`Authorization: Bearer <automation key>` at:

- `http://<hub tailnet address>:47323/mcp`: plain HTTP for agent tools that can't use a self-signed
  certificate. Tailscale encrypts it end to end; this port serves only `/mcp` and only answers
  tailnet addresses.
- `https://<hub>:47321/mcp`: the same, on the pinned HTTPS port.

| Tool | Annotations | Does |
|---|---|---|
| `list_devices` | read-only | Devices the key may use, with on/off state |
| `get_status` | read-only | One device's status (on/off, uptime, someone using it, pending action) |
| `power_on` | | Wake a device |
| `power_off` | destructive | Shut down or restart (`{device, restart?}`); always the visible 60 s countdown |
| `cancel` | | Cancel a pending shutdown/restart |

Rules the helper enforces for automation keys, whatever the agent asks:

- Only the devices and scopes on the key. Shutdown/restart need the `power` scope, granted per key.
- Refused while someone is using the device (`in_use`); there's no `force` for keys.
- One shutdown/restart per device per 10 minutes, at most 6 wakes per device per hour.
- Never the device the agent itself runs on (the helper on that device refuses, since the caller
  shares its IP's node), unless the owner ticks "allow" for that key.
- Every action is in the device's event log with the key's name, so the phone shows "Atlas shut down Workstation".

## Phone → NanoKVM

The NanoKVM's own web API (firmware 2.x), over Tailscale only:

On `401` the phone logs in again once and retries.

- `POST /api/auth/login` `{username, password}` with the password encoded as the NanoKVM UI does:
  `encodeURIComponent(CryptoJS.AES.encrypt(pw, "nanokvm-sipeed-2024"))`. That key is public, so the
  password is only as private as the channel; Tailscale encrypts it. Token: `data.token` or the
  `nano-kvm-token` cookie.
- `GET /api/vm/gpio` → `data.pwr` (power LED).
- `POST /api/vm/gpio` `{type:"power", duration:800}` short press; `duration:12000` force off.
- `POST /api/network/wol` `{mac}`.

## Updates

GitHub `releases/latest` is the source of truth. Every release has `SHA256SUMS.txt` and an Ed25519
signature (`.sig`, base64) for each helper download, checked against the public key built into the helper.

- **Windows** downloads `PowerDeck-Setup.exe` + `.sig`, verifies, runs it with `/VERYSILENT`.
- **Linux** downloads `powerdeck-helper-linux-<arch>` + `.sig`, verifies, replaces itself and restarts its service.
- **Android** downloads `PowerDeck.apk`, checks its SHA-256 against the release asset digest, installs via `PackageInstaller`; Android enforces the signing key.
