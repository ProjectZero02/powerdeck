# PowerDeck protocol (v2)

PowerDeck has three kinds of participants, all on the same [Tailscale](https://tailscale.com) network:

| Participant | Role |
|---|---|
| **Helper** (`powerdeck-helper`, Windows or Linux) | Shuts down / restarts its own machine, reports status and setup checks, sends Wake-on-LAN to its LAN, finds other PowerDeck devices on the tailnet |
| **Phone app** | Lists devices, sends commands, picks a waker for each device |
| **KVM** (NanoKVM) | Presses a computer's power and reset buttons and reads its power LED. Driven by a [hub](#kvms-on-the-hub) helper (or, without one, the phone) through the NanoKVM's own web API; nothing is installed on it. (PiKVM is recognised by discovery, but isn't driven yet.) |

## Identity and trust

Helpers listen on TCP **47321** (HTTPS, TLS 1.3, self-signed certificate) and the agent port
**47323** on **loopback and this device's Tailscale addresses only** (never `0.0.0.0`), and answer
only tailnet source addresses (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) and loopback. Tailscale addresses
are taken from Tailscale's own interface only (named for Tailscale, or carrying a `fd7a:115c:a1e0::`
address), because an ISP's carrier-grade NAT hands out `100.64.0.0/10` addresses too. Tailscale may
start after the helper or change address; the bound set is checked again every 10 s. `run --api ADDR`
/ `--mcp ADDR` (development) bind exactly the given address instead.

On every request the helper asks its local Tailscale who the caller is (`tailscale whois`, cached
for 60 s per address). A caller is one of:

| Caller | How it's recognised | Allowed |
|---|---|---|
| **Owner device** | A tailnet node on this helper's owner list (by Tailscale stable node ID) | Everything |
| **Local** | Loopback, or this node itself, with the local token (CLI, PC window) | Everything |
| **Key holder** | `Authorization: Bearer <key>` for a key this helper issued | The key's scopes |
| **Anyone else** | | `GET /v2/hello` only |

### Owner devices

A helper keeps an explicit list of owner devices (at most 20). A device gets on it by:

- **Scanning the helper's code.** The QR code (PC window, `powerdeck-helper qr`, Linux installer)
  always carries the current pairing code. `POST /v2/pair` with a valid code from an untagged node on
  this helper's own Tailscale account, not shared in, records that node as an owner device and
  returns `{owner: true, status}` with no key: Tailscale identifies it from then on.
- **`powerdeck-helper owner add NAME`** on the machine, or `POST /v2/owners {name}` by an owner: any
  untagged tailnet node that isn't shared in from another tailnet (whoever runs it already controls
  the helper). This is how a tagged helper, which has no "same account", gets an owner.
- **Claiming an upgraded helper.** A helper upgraded from 2.0 starts in *legacy* mode: as before,
  every untagged node of the same Tailscale user counts as an owner, until the first one calls
  `POST /v2/owners/claim`. That records the caller and ends legacy mode for good. The app does this
  on its own (`status.owners.legacy` is `true`). New installs never use legacy mode.

Owner devices are listed and removed in the PC window, with `powerdeck-helper owner list|rm`, in the
app's device page, and with `GET /v2/owners` / `DELETE /v2/owners/{node}`.

### Keys

- **Pairing keys**: a phone on *another* Tailscale account that enters the pairing code (8
  characters, `XXXX-XXXX`, Crockford base32, 40 bits, single use, 10 minutes, 5 wrong guesses and
  it's gone) gets a key with `status`, `wake`, `power` and `force` for **this device only**: no
  `admin`, so it can't manage keys, settings, owners or updates, relay to other devices, use `/mcp`,
  or add wake schedules. It may use the raw-MAC `/v2/wake` (a magic packet only turns things on).
- **Automation keys** (AI agents and scripts): created by an owner, from the app
  (`POST /v2/keys`), the PC window, or `powerdeck-helper key add NAME --scopes …`.
- **Peer keys** (helper to helper): kind `peer`, scopes `status`, `wake` and `power`. Helpers no
  longer reach each other "as the owner". The owner's app creates one on the target
  (`POST /v2/keys {name: <caller's name>, kind: "peer"}`, replacing an older one of that name) and
  hands it to the caller (`PUT /v2/peers/{name}/key {key}`, `name` as in the caller's
  `/v2/peers`). The caller presents it on every call to that peer. A peer key can't relay further,
  manage keys, change settings or use `/mcp`; it may carry the `X-PowerDeck-For` and
  `X-PowerDeck-Agent` relay headers. `status.peerKeys` lists the peers a helper holds a key for.

Key scopes: `status`, `wake`, `power` (shutdown/restart/cancel), `force` (shut down even while someone
is using the machine), `admin` (keys, settings, fixes). Automation keys default to `status,wake` and
can't hold `force` or `admin`. The helper stores only the SHA-256 of each key. Keys are 32 random
bytes, base64url, prefixed `pdk_`.

A key with a device list is held to it on every route, including this device's own
`/v2/power` (`403 device`), and can't use the raw-MAC `/v2/wake` (it wakes by name through the
relay). If Tailscale can't identify a caller because it isn't answering, the helper replies
`503 tailscale` (retry) rather than treating the caller as a stranger.

Automation and peer keys are **bound to the tailnet node that first uses them**: from then on the
helper accepts them only from that node (checked with `whois`), so a leaked key is useless elsewhere.
Automation keys can also be limited to named devices (see [Agents](#agents-mcp)).

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
3. **Per device**: the phone calls `GET /v2/status` on each new helper. A helper that answers
   `401 not_owner` needs the code: scanned from its QR code, or typed. For a KVM it asks for the
   KVM's password and which device's power button it's wired to, and links it by its Tailscale
   name (see [Phone → NanoKVM](#phone--nanokvm)).
4. **Linking helpers**: after each refresh the app gives every pair of helpers it owns peer keys
   for each other (see [Keys](#keys)), at most once per pair every 5 minutes. A helper whose peer
   key is refused (`401`) forgets it, so the next refresh links the pair again.

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
| GET | `/v2/keys` | owner | → `{keys:[{id, name, kind, scopes, devices, bound, createdAt, lastUsed}]}` |
| POST | `/v2/keys` | owner | `{name, scopes, devices?, allowSelf?}` → `{id, key}` (shown once). `{name, kind:"peer"}` creates a [peer key](#keys) |
| GET | `/v2/owners` | owner | → `{legacy, owners:[{node, name, addedAt, addedBy, you}]}` |
| POST | `/v2/owners` | owner | `{name}` (Tailscale name) → `{node, name}`; `404 unknown_device`, `400 not_personal` (tagged or shared in) |
| POST | `/v2/owners/claim` | owner | `{deviceName?}` → `204`; records the caller (ends legacy mode) |
| DELETE | `/v2/owners/{node}` | owner | → `204` |
| PUT | `/v2/peers/{name}/key` | owner | `{key}` → `204`: the peer key this helper presents to that peer (`""` removes it) |
| DELETE | `/v2/keys/{id}` | owner | → `204` |
| GET | `/v2/update` | owner | → `{current, latest, available, checkedAt, error, installing, auto, autoAt?, pausedUntil?}`; `?check=1` asks GitHub first. `autoAt`: when an available release installs on its own |
| POST | `/v2/update` | owner | Installs the latest signed release now → `202 {…, installing: true}`, or `200` when up to date; `409 pending` during a shutdown countdown, `502 update_check` when GitHub can't be reached. The helper restarts a few seconds later; `installing` clears itself after 10 minutes if it doesn't, with `error` saying so |
| POST | `/v2/settings` | owner | `{autoUpdate?, agents?, pauseUpdates?, hungAfterMinutes?}` → `204`. `autoUpdate` (default on for Windows, off for new Linux installs): check every 6 hours, install a release once it has been public for 72 hours and nothing is counting down (see [Updates](#updates)). `pauseUpdates`: `true` holds automatic installs for 7 days, `false` resumes. `agents` (default on): `false` refuses `wake`, `shutdown` and `restart` from automation keys for this device, here and through any hub (`403 agents_off`); status and cancel still work. `hungAfterMinutes` (on a hub; 2 to 30, `0` for the default, 3): how long a machine must look hung before it's called [`hung`](#machine-states-and-events). `autoResetMinutes` (on a hub, 2.4 and later; 5 to 60, `0` off, the default): confirm the hub's own reset proposal after that many minutes, see [Auto-reset](#auto-reset); refused for a hub relaying for an agent (`403 owner_only`) |
| POST | `/v2/uninstall` | owner | `{confirm: <this device's name>}` → `202`, then the helper uninstalls itself and deletes its keys and settings. `409 pending` during a countdown. Offered when `caps` has `uninstall` |
| GET | `/v2/pairing` | owner | → `{link, code, expires}`: the add link and the current pairing code (to let someone on another account in) |
| POST | `/v2/pair` | pairing code | `{code, deviceName}` → `{owner:true, status}` (same account: now an owner device) or `{key, status}` (another account) |
| DELETE | `/v2/key` | key holder | → `204`; revokes the caller's own key |
| POST | `/v2/devices/{name}/{action}` | per action | Relay: the helper performs `action` (`status`, `wake`, `shutdown`, `restart`, `cancel`; `reset`, `force-off` through a KVM here) on another PowerDeck device it knows, see [Agents](#agents-mcp). `502 peer_refused` when that device doesn't accept this helper (no or a revoked peer key). With a KVM here, `wake`, `shutdown` and `restart` go through it too and answer like [`/v2/machines`](#kvms-on-the-hub) (the machine's helper's own reply fields included) |
| GET | `/v2/kvms` | owner | → `{kvms:[KVM]}`, see [KVMs on the hub](#kvms-on-the-hub). Offered when `caps` has `kvm` |
| POST | `/v2/kvms` | owner | `{address, password, machine, user?, name?, mac?, model?, helper?}` → `201 {kvm, led}`; logs in and reads the power light before storing it |
| PATCH | `/v2/kvms/{name}` | owner | `{machine?, mac?, user?, password?, recheck?}` → `{kvm}`. `recheck: true` forgets what was learned about the wiring |
| DELETE | `/v2/kvms/{name}` | owner | → `204` |
| GET | `/v2/machines` | `status` | → `{machines:[Machine]}`: the devices the caller may use, with live state; `?kvm=1` only those with a KVM here |
| POST | `/v2/machines/{name}/{action}` | per action | `{force?, stayOff?, press?}` → `200 Result`, or the error with the Result's fields. `action`: `wake`, `shutdown`, `restart`, `reset`, `force-off` |
| POST | `/v2/machines/{name}/recoveries` | `power` | `{action?, reason?}` → `201 {recovery}`, or `200 {recovery}` when one is open for the machine already. Proposes a reset (the default) or `force-off` for the owner to confirm, see [Recoveries](#recoveries) (2.4 and later) |
| GET | `/v2/recoveries` | `status` | → `{recoveries:[Recovery]}`, newest first; `?open=1` only those `proposed` or `running` |
| GET | `/v2/recoveries/{id}` | `status` | → `{recovery}` |
| POST | `/v2/recoveries/{id}/confirm` | owner device | → `202 {recovery}` (now `running`), or `200 {recovery}` as it is when it isn't `proposed` any more |
| POST | `/v2/recoveries/{id}/decline` | owner device, or the key that proposed it | → `200 {recovery}`: `declined`, or `withdrawn` by its proposer |
| GET | `/v2/machines/events` | `status` | Server-Sent Events: each machine's state as it changes, see [Machine states and events](#machine-states-and-events) (2.3 and later) |
| POST | `/v2/note` | peer key | `{event, updates?}` → `204`: a machine's own helper telling its hub it's about to `sleep`, `shutdown` or `restart`, or did `resume` |
| GET | `/v2/webhooks` | owner | → `{webhooks:[Webhook]}` (without secrets) |
| POST | `/v2/webhooks` | owner | `{url, events?}` → `201 {webhook}` with its `secret`, shown this once. `400 bad_url`, `400 bad_event`, `409 too_many` (10 at most) |
| DELETE | `/v2/webhooks/{id}` | owner | → `204` |
| POST | `/v2/webhooks/{id}/test` | owner | Sends a made-up `hung` event for a machine called `test` → `{ok, result}` |
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
  "caps": ["shutdown", "restart", "wake", "kvm", "uninstall"],
  "sleep": {"s3": true, "modernStandby": false, "afterMinutes": 30},
  "hub": {"hungAfterMinutes": 3, "autoResetMinutes": 0},
  "checks": [Check],
  "agents": {"allowed": true, "keys": [{"name": "Atlas", "devices": ["Workstation", "nas"]}]},
  "you": {"kind": "owner", "name": "Pixel 9 Pro", "scopes": ["status", "wake", "power", "force", "admin"]}
}
```

`guests` appears on Proxmox hosts. The `kvm` cap means this helper can drive NanoKVMs (`/v2/kvms`, `/v2/machines`; 2.2 and later). `agents` and the `uninstall` cap are shown to owners only; `agents.keys` lists the automation keys this helper holds and the devices each may use. `updatesPending: true` (Windows) means "Update and shut down" is available. `presence` is omitted when the helper can't tell. `sleep` (Windows, 2.3 and later): whether the PC can sleep the classic way (S3) or uses Modern Standby, and after how many idle minutes on AC power it sleeps on its own (0: never); a hub uses it to tell sleep from a hang. `hub` is shown to owners on a helper that drives KVMs.

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
| **KVM** wired to the device's front panel (power button, reset button, power LED; one KVM per device) | Short press, **only while the LED says off** (primary when present) | Short press = the OS's own clean shutdown (fallback when the helper doesn't answer); reset press, or a 12 s hold then a press, for a frozen device | Power LED (fallback) |
| **Wake-on-LAN** sent by a waker on the device's LAN | Magic packet (primary without a KVM, fallback with one) | | |

A KVM is linked to a device when it's added ("Which device's power button is this NanoKVM wired
to?"). An always-on helper (the **hub**) then drives it, see [KVMs on the hub](#kvms-on-the-hub); the
phone drives it itself only when there's no hub. The LED is read before and after every press,
so a press can never turn a running machine off by mistake.

### Wakers

Tailscale can't carry Wake-on-LAN broadcasts, so something already on, on the same LAN, sends them.
For each device the phone remembers its MAC and LAN subnets (from its last status) and tries, in order:

1. a helper whose `network.lan` overlaps the device's (`POST /v2/wake`);
2. a KVM on that LAN (NanoKVM `POST /api/network/wol`);
3. the phone itself, if it's on that LAN (UDP broadcast).

The magic packet is sent 3 times, to the subnet broadcast and to 255.255.255.255. Afterwards the
phone polls until the device's helper (or the KVM's LED) says it's on, and reports how long it took.

## KVMs on the hub

From 2.2 the app hands each NanoKVM to one always-on helper, the **hub** (by default its only Linux
helper, typically a home server; the app's **Settings → KVM hub** picks another or keeps KVMs on the
phone). The hub then reaches the NanoKVM over Tailscale with the same [web API](#phone--nanokvm),
for the app and for [agents](#agents-mcp) alike, and learns the wiring on its own. The app sends the
NanoKVM password once, over the pinned TLS connection; the hub stores it sealed (see
[SECURITY.md](../SECURITY.md#where-secrets-live)). Removing the KVM in the app deletes it from the hub.
When the hub can't be reached, the phone falls back to driving the KVM itself.

A KVM's address must be a `*.ts.net` name or a Tailscale address (`400 not_tailscale` otherwise), so
its password only travels inside the tailnet. A hub can't drive the KVM wired to itself
(`422 self`): it couldn't check the result of a reset. One KVM per machine (`409 machine_has_kvm`),
at most 20 per hub. `helper: true` when linking says the machine runs a PowerDeck helper, which the
hub must already know (`422 unknown_helper` otherwise): without it a reset couldn't first check
that the machine stopped answering.

### KVM

```json
{"name": "kvm-workstation", "address": "http://kvm-workstation.example.ts.net", "user": "powerdeck",
 "machine": "Workstation", "mac": "02:00:5e:00:00:0a", "firmware": "2.5.2 Beta",
 "wiring": {"led": "wired", "power": "unknown", "reset": "unknown", "checkedAt": "…"},
 "addedAt": "2026-10-27T08:00:00Z"}
```

The password is never returned.

### Wiring is learned, never assumed

Each of `led`, `power` and `reset` is `unknown`, `wired` or `not_wired`. Nothing has to be declared,
and nothing is pressed on a guess:

| Part | Wired when | Not wired when |
|---|---|---|
| Power LED | It reads on once (an unplugged or reversed LED never reads on) | It reads off 3 times, at least 30 s apart, while the machine's helper answers |
| Power switch | A press or hold changes the LED | A press or hold doesn't (`power_not_wired`, `no_effect`) |
| Reset switch | The machine's helper answers again within 5 minutes of a reset | It doesn't: the next reset is refused (`reset_not_wired`) unless forced |

The hub reads every KVM's LED every 30 seconds (see [Machine states and events](#machine-states-and-events)),
so the LED is usually learned without anyone pressing anything. `wiring.ledAsleep` is what the light
does while the machine sleeps (`off`, `blinking` or `on`), read 30 s after its helper said it was
going to sleep. `PATCH {recheck: true}` (the app's **Check again**) forgets what was learned.

### Machine

```json
{"name": "Workstation", "helper": true, "os": "windows", "mac": "02:00:5e:00:00:0a",
 "lan": ["192.168.1.0/24"], "kvm": KVM, "helperAnswers": false, "led": true,
 "state": "unknown", "actions": ["wake", "shutdown", "restart", "reset", "force-off"]}
```

`state`: `on`, `off`, `hung` or `unknown`, with `reason`, `asleep`, `since` and `evidence` from the
hub's watch (see [Machine states and events](#machine-states-and-events)). `recovery` (2.4 and
later) is the machine's open [recovery](#recoveries), or the last one for 30 minutes after it ended. `led` is the power
light right now (absent without a KVM, `ledError` when it can't be read). `actions` is what the hub
can do for it now; `reset` and `force-off` appear only while a wired LED reads on.

### Actions

`POST /v2/machines/{name}/{action}` and the relay (`/v2/devices/{name}/{action}`) run the same code:

| Action | What the hub does |
|---|---|
| `wake` | Nothing if the helper answers or the LED is on. Else a short power press when a wired LED reads off, checking that the LED comes on; else Wake-on-LAN from the hub's LAN, then from the KVM |
| `shutdown`, `restart` | Asks the machine's helper (60 s countdown, its in-use check applies). `press: true`, or no helper: a short power press for a clean OS shutdown, only while a wired LED reads on, **owner devices only** (nobody can check whether someone is using the machine). `restart` always needs the helper |
| `reset` | Refused while the helper answers (`409 helper_answers`) unless `force`. Reads the LED (must be wired and on), presses reset, reads the LED again, then, when the helper wasn't answering, checks in the background that it answers again within 5 minutes |
| `force-off` | Same checks. Holds power for 12 s, waits for the LED to go off, then (unless `stayOff`) waits 5 s and presses power to start it again, waiting for the LED to come on |

One action per machine at a time (`409 busy`), at most 2 minutes. An action runs to its end even when
the caller hangs up, so a machine held off is also started again. A KVM whose stored password can't
be opened (`kvm_credentials`: link it again) still lets the machine's helper wake, shut down or
restart it. Nothing is learned about the wiring from a reading that failed. **`reset` and `force-off` are for owner devices
only** (`403 owner_only`), never an automation key or an agent relayed through a hub; agents
[propose](#recoveries) one instead, and the owner confirms it. Each press of either button is also
recorded as a recovery (`direct: true`), so it counts towards the 30-minute limit.

Result (also in the error body on failure, with `error` and `message` added):

```json
{"machine": "Workstation", "action": "reset", "ok": true, "message": "Pressed reset on Workstation. It should answer again in a few minutes.",
 "steps": [{"at": "…", "do": "read power light before", "led": true},
           {"at": "…", "do": "press reset 800ms"},
           {"at": "…", "do": "read power light after", "led": true}]}
```

`code` on success: `already_on`, `led_on`, `already_off`, or none. Refusals: `helper_answers`,
`led_unreadable`, `led_untrusted` (the LED reads off and hasn't been seen on), `already_off`,
`power_not_wired`, `reset_not_wired`, `no_effect`, `didnt_start`, `cant_wake`, `no_helper`,
`helper_down`, `owner_only`, `busy`, and the KVM's own errors: `kvm_password`, `kvm_locked`,
`kvm_forbidden` (the account lacks the power permission), `kvm_cert_changed`, `kvm_unreachable`,
`kvm_failed`. Every action is in the hub's event log with who asked and the outcome.

## Machine states and events

From 2.3 a hub looks at every machine with a KVM every 30 seconds and tells a hung machine apart from
one that's booting, asleep, installing updates, or just missing its helper. Each look gathers, at
once and in 8 s at most:

| Evidence | Values | What it says |
|---|---|---|
| `helper` | `answers`, `silent` | The machine's PowerDeck helper answered its status (two tries, 1.5 s apart) |
| `port` | `refuses`, `accepts`, `no_reply` | A TCP connection to the helper's port. `refuses`: the OS is up and answered. `accepts`: the kernel took it (a hung Windows still does) |
| `ping` | `replies`, `no_reply`, `unavailable` | `tailscale ping --icmp` to the machine: answered by its own network stack, not by Tailscale alone. `unavailable` when the hub can't ping |
| `led` | `on`, `off`, `blinking`, `unreadable` | Three readings of the power light 0.5 s apart; on and off in one look is `blinking` |
| `ledTrusted` | `true`, `false` | The light is [wired](#wiring-is-learned-never-assumed) |
| `tailnetOnline` | `true`, `false` | Tailscale's control plane lists the machine online. Shown, never decides: a hung PC stays online there for minutes |

What the machine's helper said also counts: its last status (a shutdown or restart counting down,
Windows updates pending, its [`sleep`](#status) facts) and its notes. A helper tells every hub it
has a peer key for, just before its machine sleeps, shuts down or restarts, and when it wakes
(`POST /v2/note`, best effort, 1.5 s). The hub works out which machine from the caller's address,
so a helper only speaks for its own.

**States:**

- `on`: the helper answers.
- `off`: a wired light reads off. `asleep: true` when it blinks, or after a `sleep` note.
- `hung`: for `hungAfter` (3 minutes; the owner's `hungAfterMinutes` setting, 2 to 30) every look
  saw the helper silent, the port not refusing, ping not replying (or unavailable), and a wired
  light steadily on; and nothing excused it (below). A machine without a helper or without a wired
  light is never `hung`.
- `unknown`, with a `reason`:

| `reason` | When |
|---|---|
| `no_helper` | No PowerDeck helper on the machine: nothing can say it's fine |
| `led_not_trusted` | The light hasn't been seen on yet |
| `led_unreadable` | The KVM didn't answer this look |
| `checking` | Silent, light on, for less than `hungAfter` |
| `helper_stopped` | Silent while ping replies or the port refuses: the machine runs, its helper doesn't |
| `booting` | Within 10 minutes of the hub waking, restarting, resetting or forcing it off, or of its light coming on |
| `shutting_down`, `restarting` | Within 10 minutes of a shutdown or restart note or countdown |
| `updating` | The same, with Windows updates pending: 90 minutes (they install with the light on and the network down) |
| `may_be_asleep` | After a `sleep` note, or the machine sleeps on its own after `afterMinutes` and its light while asleep is `on` or not learned yet |

Anything that contradicts a hang (the helper answers, ping replies, the port refuses, the light is
off or blinking) starts the count again. An unreadable light neither counts nor resets it, and a
gap of more than `hungAfter` between looks (the hub was off) starts it again too.

`since` is when the hub first saw the current state; `evidence` is its last look, with `at`.

### Event stream

`GET /v2/machines/events` (status scope; the machines with a KVM on this hub that the caller may
use, as in `GET /v2/machines?kvm=1`) is a
[Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html) stream:

```
id: 1761638400000123
event: snapshot
data: {"machines":[Machine, …]}

id: 1761638430000456
event: machine
data: {"id":"1761638430000456","at":"2026-10-28T08:00:30Z","event":"hung","machine":"Workstation","state":"hung","from":"unknown","evidence":{"at":"…","helper":"silent","port":"accepts","ping":"no_reply","led":"on","ledTrusted":true}}

: still here
```

Each `machine` event is a state change: `event` is the new state, or `recovered` when a `hung`
machine answers again (`state` is then `on`). `reason` and `asleep` are included when set. IDs
grow, also across restarts of the hub. Reconnect with `Last-Event-ID` (or `?lastEventId=` for
clients that can't set headers) to get what came after it, from the last 500 the hub holds; an ID
it no longer holds gets a fresh snapshot. A comment every 25 s keeps proxies from closing the
stream. A client that stops reading for 10 s is dropped, and so is one that falls 64 events behind
(it reconnects and replays). At most 16 streams per hub (`503 busy`).

`hung` and `recovered` also go to the hub's event log (`GET /v2/events`), which the phone
[notifies](#notifications) about.

From 2.4 the stream also carries `recovery` events, one each time a [recovery](#recoveries) changes
state, and the snapshot lists the open ones (`{"machines": [...], "recoveries": [Recovery, …]}`):

```
id: 1761638430000789
event: recovery
data: {"id":"1761638430000789","at":"…","event":"recovery","machine":"Workstation","recovery":{"id":"…","state":"proposed",…}}
```

### Webhooks

For an assistant that would rather be called than hold a stream open. An owner device adds one:

```json
POST /v2/webhooks
{"url": "https://assistant.example.com/powerdeck", "events": ["hung", "recovered"]}

201 {"webhook": {"id": "c0ffee…", "url": "…", "events": ["hung", "recovered"],
                 "createdBy": "Pixel 9 Pro", "createdAt": "…", "secret": "pdwh_…"}}
```

`events` defaults to `hung`, `recovered` and `recovery` (any of `on`, `off`, `hung`, `unknown`,
`recovered`, `recovery`; `recovery` from 2.4).
The URL must be https, or plain http to a Tailscale or loopback address or a `*.ts.net` name
(WireGuard already encrypts those); no user names in it. The secret is shown this once and stored
sealed, like KVM passwords. The list shows each webhook's `lastAt` and `lastResult` (`ok`,
`HTTP 500`, a network error).

Each delivery is a POST of the [event](#event-stream) as JSON, with:

```
X-PowerDeck-Event: hung
X-PowerDeck-Delivery: 1761638430000456      (the event's id; the same on every retry)
X-PowerDeck-Timestamp: 1761638431           (Unix seconds of this attempt)
X-PowerDeck-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>
```

Any 2xx is delivered. Otherwise it's tried again after 10 s, 1 minute and 5 minutes, then dropped
and logged. 10 s per try; redirects aren't followed. A receiver checks the signature over the raw
body, refuses a timestamp more than 5 minutes off (so an old delivery can't be replayed), and can
use the delivery ID to ignore repeats:

```python
import hashlib, hmac, time

def verify(secret: str, headers, body: bytes) -> bool:
    ts = headers["X-PowerDeck-Timestamp"]
    if abs(time.time() - int(ts)) > 300:
        return False
    mac = hmac.new(secret.encode(), ts.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest("sha256=" + mac, headers["X-PowerDeck-Signature"])
```

To check an implementation: secret `pdwh_test`, timestamp `1700000000` and body `{"id":"1"}` sign as
`sha256=8fc0a23c6a794d26f2a6c83ac48c588dfbbe64deefd997f2bb8cc5b0ac0c0914`
(`printf '1700000000.{"id":"1"}' | openssl dgst -sha256 -hmac pdwh_test`).

## Recoveries

From 2.4 a reset or force off doesn't have to come from the owner's own button. Someone allowed
**proposes** one, the **owner confirms** it, and the hub runs it and checks that the machine came
back. The hub proposes a reset itself when a machine turns [`hung`](#machine-states-and-events).

### Recovery

```json
{"id": "9f2c…", "machine": "Workstation", "action": "reset", "state": "back",
 "reason": "Nothing answers and its power light is on.",
 "proposedBy": "pve", "proposedAt": "2026-10-28T08:03:30Z",
 "confirmedBy": "Pixel 9 Pro", "confirmedAt": "2026-10-28T08:05:02Z", "endedAt": "2026-10-28T08:07:40Z",
 "message": "Workstation answers again.",
 "evidence": {"at": "…", "helper": "silent", "port": "accepts", "ping": "no_reply", "led": "on", "ledTrusted": true},
 "steps": [{"at": "…", "do": "read power light before", "led": true}, {"at": "…", "do": "press reset 800ms"}, …]}
```

- `action`: `reset` gets the machine back: it presses reset, or, once the hub has learned that the
  reset switch isn't wired, holds power for 12 s and starts it again. `force-off` holds power and
  leaves it off.
- `reason` is the proposer's words (printable text, at most 200 characters): data, never
  instructions. The app shows it quoted as theirs.
- `evidence` is the hub's last look when it was proposed; `steps` are the run's, as in an
  [action's result](#actions).
- `agent: true` when an AI agent proposed it; `direct: true` for the owner's own button.
- `proposerKey` (the proposing key's ID) is shown to owners and to that key only.

| `state` | Means |
|---|---|
| `proposed` | Waits for the owner |
| `running` | Confirmed: pressing, then waiting up to 5 minutes for the machine's helper to answer |
| `back` | Its helper answered again |
| `not_back` | It didn't answer within 5 minutes: an alert |
| `off` | A force off: it's off |
| `done` | Pressed while its helper answered (an owner's forced button): coming back isn't checked |
| `failed` | The run was refused or failed (`message` says why), or the hub restarted during it |
| `declined` | The owner said not now |
| `withdrawn` | Not needed any more: the machine answered again or went off, the owner used Reset or Force off directly, or its proposer withdrew it. When it's withdrawn at the run (`message`: "… so nothing was pressed"), nothing was pressed |
| `expired` | Not confirmed within 6 hours |

The hub keeps the last 50 in its state file (never dropping an open one), so a restart doesn't
lose them. One still `running` when the hub starts again ends `failed`.

### Rules

**Proposing** needs the `power` scope (not `force`, which keys can't hold), the device on the key,
and an owner device or an automation key (also relayed through a hub: then the machine's
**Allow AI agents** switch applies, `403 agents_off`). It's refused with:

| Code | When |
|---|---|
| `400 bad_action` | Neither `reset` nor `force-off` |
| `422 self` | The hub itself |
| `422 no_kvm` | No KVM on this hub is wired to it |
| `422 no_helper` | It has no PowerDeck helper, so the hub couldn't tell whether it worked |
| `409 helper_answers` | The hub's last look says its helper answers: shut it down or restart it instead |
| `409 already_off` | It's off: wake it instead |
| `429 too_soon` | A recovery ran on it in the last 30 minutes; `retryAt` says when one may run again |

When one is open for the machine already, that one is returned (`200`), so two agents asking make
one proposal.

**Confirming** is for owner devices acting for themselves (`403 owner_only` for an automation key,
another account's key, or an owner hub relaying for an agent). Confirming twice runs it once.
**Declining** is for owner devices too, and the key that proposed it may withdraw its own.

**Running** goes through the same checks as the [buttons](#actions): a wired power light that reads
on, and a helper that doesn't answer. A machine that came back in the meantime isn't touched.

**At most one recovery runs per machine per 30 minutes.** The owner's own Reset and Force off
buttons count towards this but are never held back by it: pressing one is the owner confirming.

**The hub proposes a reset** when a machine turns `hung`, once per hang, if nothing is open for it
and none ran in the last 30 minutes. A declined or expired one isn't proposed again until the
machine has left `hung`. Each look also withdraws an open proposal when the machine answers or is
off, and expires one after 6 hours.

### Auto-reset

`autoResetMinutes` (owner setting, `POST /v2/settings`; app: the hub's page → **Frozen machines**):
`0`, the default, asks every time. Set to 5 to 60, the hub confirms a `reset` proposal itself that
many minutes after it was made, if the machine is still `hung` by its own evidence at that moment.
`confirmedBy` is then `PowerDeck (automatic)`. Never for `force-off`, and never for a machine that's
booting, updating, asleep or otherwise not `hung`.

### Log, events and webhooks

The hub's event log gets one line per step, with who: `propose reset Workstation: <reason>`
(`proposed`), `confirm reset Workstation` (`confirmed`), `decline …` (`declined`), `withdraw …`
(`withdrawn`), `Workstation answers again after the reset` (`back`), `Workstation didn't answer
within 5 minutes of the reset` (`not_back`), `Workstation is off` (`off`), and `reset Workstation`
with `failed: …`, `withdrawn: …` or `expired: …`. For the owner's direct buttons the button's own
line (`reset Workstation`, `done`) is followed by `back` or `not_back`.

Every change is also an event `{"event": "recovery", "machine", "recovery": Recovery}` on the
[event stream](#event-stream) and to [webhooks](#webhooks) that list `recovery`.

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
a device going offline unexpectedly, and (from a 2.3 hub) a machine that
[looks frozen](#machine-states-and-events) or answers again. Actions started on the phone show live progress instead.

From a 2.4 hub, a [proposed recovery](#recoveries) comes with **Reset** (or **Force off**) and
**Not now**, which work only on an unlocked phone; it replaces the plain "looks frozen"
notification for that machine. A recovery that's confirmed on its own or by someone else, comes
back, doesn't come back (on the alerting **Frozen machines** channel) or fails is notified too.

## Agents (MCP)

AI agents and scripts talk to **one always-on helper** (a "hub", e.g. a home server). The hub
relays to the other devices, because a PC that's off can't answer for itself: it wakes devices
through its own LAN and forwards shutdown/restart/status to their helpers with its
[peer key](#keys) for each (helpers before 2.1 relied on being on the same Tailscale account
instead). When it relays for an agent it adds `X-PowerDeck-For: <key name>` (shown in the target's
history) and `X-PowerDeck-Agent: 1`, so the target applies its own `agents` switch too. Both
headers are honoured from owner devices and peer keys only. Helpers before 2.0.3 don't send or check `X-PowerDeck-Agent`; there the
hub's remembered copy of the switch (refreshed with `GET /v2/peers?refresh=1`) is the only check.

The hub serves MCP (streamable HTTP, stateless JSON-RPC `tools/list` and `tools/call`) with
`Authorization: Bearer <automation key>` at:

- `https://<hub>:47321/mcp`: **recommended**, on the pinned HTTPS port.
- `http://<hub tailnet address>:47323/mcp`: plain HTTP, kept for agent tools that can't pin a
  self-signed certificate. The key crosses it in the clear *inside* Tailscale: the port is bound to
  the Tailscale addresses (and loopback) only, so nothing outside the tailnet can even connect, and
  WireGuard encrypts every packet end to end between the two nodes. It serves only `/mcp`. Anyone
  who can read traffic on the hub or the agent's machine themselves could read the key, which is
  also true of the key in the agent's config; keys are bound to one node, so a copy is useless elsewhere.

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
- No `reset` or `force-off` (`403 owner_only`), even relayed through an owner device. From 2.4 an
  agent [proposes](#recoveries) one (`POST /v2/machines/{name}/recoveries`, `power` scope) and the
  owner confirms it on the phone. Where the hub
  drives a machine's KVM, `power_on` may press its power button (only while a wired LED reads off),
  and `power_off` goes to the machine's helper; pressing power to shut it down is owner-only.

## Phone → NanoKVM

The NanoKVM's own web API (firmware 2.x), over Tailscale only. The [hub](#kvms-on-the-hub) uses it
the same way; the phone uses it directly only without a hub, or when the hub can't be reached. The
app reaches a KVM by its
**Tailscale (MagicDNS) name**, `<name>.<tailnet>.ts.net`: Android allows plain HTTP only to
`*.ts.net` names (`network_security_config.xml`), and the app refuses other addresses. KVMs linked
by IP address before 2.1 are renamed automatically from the helpers' peer lists.

On `401` the phone logs in again once and retries.

- `POST /api/auth/login` `{username, password}` with the password encoded as the NanoKVM UI does:
  `encodeURIComponent(CryptoJS.AES.encrypt(pw, "nanokvm-sipeed-2024"))`. That key is public, so the
  password is only as private as the channel; Tailscale encrypts it. Token: the `nano-kvm-token`
  cookie (HttpOnly from 2.5; up to 2.4.x also `data.token`).
- `GET /api/vm/gpio` → `data.pwr` (power LED).
- `POST /api/vm/gpio` `{type:"power", duration:800}` short press; `duration:12000` force off;
  `{type:"reset", duration:800}` reset.
- `GET /api/vm/info` and `GET /api/vm/hardware` → firmware and board version (the hub stores them).

A NanoKVM in HTTPS mode redirects port 80 to HTTPS; the hub follows that once and pins the
certificate it finds. See [nanokvm-hardening.md](nanokvm-hardening.md) for the account to give PowerDeck.
- `POST /api/network/wol` `{mac}`.

## Updates

GitHub `releases/latest` is the source of truth.

**Staged.** A helper installs a release on its own only once it has been public for **72 hours**
(GitHub's `published_at`, or when the helper first saw it if that's later), so a bad or forged
release can be pulled before most helpers take it. Owners can pause automatic installs for 7 days
(`pauseUpdates`). Installing from the app or the PC window doesn't wait. Automatic updates are on by
default on Windows and **off for new Linux installs** (servers); an upgrade keeps the existing
setting. See [SECURITY.md](../SECURITY.md) for what the signing key protects and what it doesn't. Every release has `SHA256SUMS.txt` and an Ed25519
signature (`.sig`, base64) for each helper download, checked against the public key built into the helper.

- **Windows** downloads `PowerDeck-Setup.exe` + `.sig`, verifies, runs it with `/VERYSILENT`.
- **Linux** downloads `powerdeck-helper-linux-<arch>` + `.sig`, verifies, replaces itself and restarts its service.
- **Android** downloads `PowerDeck.apk`, checks its SHA-256 against the release asset digest, installs via `PackageInstaller`; Android enforces the signing key.
