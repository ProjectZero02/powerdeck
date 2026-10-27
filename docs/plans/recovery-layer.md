# Plan: PowerDeck as the power and recovery layer

Goal: PowerDeck can turn every machine on and off **and get a hung one back**, from the phone or from
an AI assistant, without anyone holding the power button. An assistant can use a PowerDeck hub
directly, or run its own copy.

The failure this is for: Windows hangs but the PC stays powered. The network card still answers ARP,
Tailscale's control plane still lists the node as online, and the TCP stack may still accept
connections, but nothing answers: no ping, no RDP, no file sharing, no PowerDeck helper (it hangs
with the PC). Only a reset or a long press on the power button brings it back, so PowerDeck needs a
NanoKVM wired to the front panel, driven from an always-on hub.

One pull request per phase. Each phase starts by updating its section below.

## Facts this plan relies on (NanoKVM, from upstream source)

- Models: Full/Cube (ATX board over USB-C), PCIe (front-panel header) and Pro (Desk/ATX) can press
  **power** and **reset** and read the **power LED**. The **Lite has no ATX board**.
- `POST /api/auth/login`: password AES-encrypted with the public passphrase `nanokvm-sipeed-2024`
  (Pro: plain password). The session is the `nano-kvm-token` cookie (≤ 2.4.x also in the body).
- `GET /api/vm/gpio` → `{pwr, hdd}`, read live at request time (no latching; `hdd` only on early hardware).
- `POST /api/vm/gpio {type: "power"|"reset", duration}`: duration in ms (0 = 800). The reply comes
  after the press ends, so a 12 s hold keeps the request open for 12 s.
- `POST /api/network/wol {mac}`: the KVM sends the magic packet on its own LAN.
- `GET /api/vm/info` (application/image version), `GET /api/vm/hardware` (Alpha/Beta/PCIE/Pro).
- No unauthenticated health endpoint; `GET /` (the web UI) is the only probe without a login.
- Firmware with HTTPS enabled redirects port 80 to a self-signed HTTPS port.

## Phase 1: security groundwork (before KVM control moves to the hub)

Build:
1. **Owner devices instead of "same Tailscale user".** A helper keeps an explicit list of owner
   nodes. A phone becomes an owner by scanning the helper's code (it carries a one-time secret, valid
   10 minutes), or with `powerdeck-helper owner add <tailscale name>` on the machine. Owners can be
   listed and removed in the PC window, the CLI and the app.
   - Upgrades: an existing helper keeps the old rule only until its first owner device is
     recorded. The first untagged same-user node to claim it while none is recorded becomes the
     owner, so an updated app claims it on first contact, and after that the rule is gone. New
     installs never use the old rule.
   - Pairing: the code is now in every add QR code. `POST /v2/pair` with a valid code records a
     same-account phone as an **owner device** (no key). A phone on another account gets a key
     with `status`, `wake`, `power` and `force`, but no longer `admin`, for that device only.
   - Helpers stop calling each other "as the owner". Each helper holds a **peer key** for every
     other helper it talks to: kind `peer`, scopes `status`, `wake` and `power`, bound to the
     calling node on first use. Peer keys can't relay further, manage keys or change settings. The
     app, as owner of both helpers, creates them on its own (`POST /v2/keys {kind:"peer"}` on the
     target, then `PUT /v2/peers/{name}/key` on the caller).
2. **Listen on the Tailscale addresses only** (API 47321, agent port 47323), following them when
   Tailscale starts late or changes address. Loopback stays for the machine's own tools.
3. **Agent port without TLS**: keep it for MCP clients that can't pin a self-signed certificate,
   but only on the tailnet addresses (WireGuard encrypts it end to end), and document that. HTTPS
   `/mcp` on 47321 is the recommended endpoint.
4. **Android: cleartext only for KVMs.** No cleartext by default; allowed only for `*.ts.net`
   names, where NanoKVMs are reached. KVM addresses must be Tailscale names (the form says so, and
   old IP entries are flagged).
5. **Automatic updates, staged.** A release is installed automatically only after it has been
   public for 72 hours, so a bad or forged release can be pulled first. Pause for 7 days from the
   app or PC window. New Linux installs (servers) start with automatic updates **off**; existing
   settings are kept. SECURITY.md describes the signing-key risk and what a stolen key could do.

Test:
- Unit: owner list and claim (secret, expiry, single use, legacy claim closes after the first
  owner), hub-key scopes, agents and pairing keys unchanged, listener address selection and
  rebinding, update staging and pause.
- Android: Robolectric tests for the claim call and the KVM address rule; the network security
  config is checked by a test that parses it.
- CI end to end (Linux and Windows): after install, nothing listens on 0.0.0.0/:: for 47321/47323.

## Phase 2: KVM control on the hub, with a fake NanoKVM

Build:
1. **NanoKVM client in the helper** (`internal/nanokvm`): login with the AES-encrypted password
   (Pro: plain), session from the `nano-kvm-token` cookie or (≤ 2.4) the body, one re-login on 401,
   `403` as a permission error, `-2` bad password, `-4`/`-5` locked. Power LED, power and reset
   presses (the read timeout covers the press), Wake-on-LAN, firmware info. A KVM that redirects to
   HTTPS on the same host is followed, and its self-signed certificate is pinned on first use; a
   changed certificate is refused until the KVM is linked again.
2. **Fake NanoKVM** (`internal/nanokvm/fake`, runnable as `go run ./cmd/fakekvm` for app work): the
   firmware's routes and replies, a simulated machine behind it (power LED follows presses after a
   boot or shutdown delay, a hung machine ignores a short press, reset reboots, a hold of 4 s or more
   cuts power), lockout after failed logins, 2.4/2.5/Pro login styles, Wake-on-LAN recording, and
   failure modes: LED not wired, power or reset switch not wired, unreachable, HTTPS redirect, 403.
3. **Machine map on the hub.** A helper keeps KVM records: address (a Tailscale name), model,
   user, password, the machine it's wired to (a helper it knows, or a name and MAC for a machine
   without one), and what it learned about the wiring. Passwords are sealed at rest: DPAPI (the
   service account's scope) on Windows; on Linux AES-256-GCM with a 32-byte key in
   `/etc/powerdeck/secret.key` (root, 0600), apart from the data in `/var/lib/powerdeck`. The
   hardening guide says what that protects (a copied state file or backup of the data folder) and
   what it doesn't (root on the hub).
4. **Actions** (`POST /v2/machines/{name}/{action}`), the power LED read before and after each
   one and returned as steps; one action per machine at a time:
   - `wake`: done if the helper answers. LED trusted and off → power press, LED checked for 15 s;
     otherwise (LED on, untrusted or unreadable) no press, Wake-on-LAN from a helper on the
     machine's LAN, or from its KVM.
   - `shutdown`, `restart`: through the machine's helper with its usual countdown. If the helper
     doesn't answer, the reply says so and points to reset (no press).
   - `reset` (owner devices only until phase 4): the LED must be trusted and on; refused while the
     helper answers unless `force`. Reset press, LED read after.
   - `force-off` (owner devices only until phase 4): the LED must be trusted and on. 12 s hold, LED
     must read off within 15 s, then (unless `stayOff`) a power press and the LED must read on.
   No press when the LED can't be read or isn't trusted: never blind.
5. **Wiring learned, not configured.** The hub reads each KVM's LED every 2 minutes and compares it
   with the machine's helper. LED on while the helper answers → LED wired. LED off while the helper
   answers, three times in a row → not wired or reversed (an LED wired backwards never lights), not
   trusted for "off". Power switch: wired when a press turns the LED on, or a 12 s hold turns it off;
   not wired when neither happens. Reset switch: the power light stays on through a reset, so the hub
   can't see one happen; a reset is confirmed when the helper answers again within 5 minutes, and
   marked as having no effect otherwise (recovery then uses force off and power on). Shown with the
   machine; `PATCH /v2/kvms/{name} {recheck:true}` forgets it.
6. **API:** `GET/POST /v2/kvms`, `PATCH/DELETE /v2/kvms/{name}` (owner devices), `GET /v2/machines`
   (status scope: every machine with its helper, KVM, MAC, LAN, wiring and live LED), the actions
   above. Relayed and MCP `wake` for a machine with a KVM on this helper go through the same path.
7. **App:** a helper that has the `kvm` capability can hold KVMs. When the owner has one (Settings
   → **KVM hub**, chosen automatically when there's exactly one Linux helper), linking a KVM stores
   it there, and existing KVM links move there on their own. Device actions go through the hub when
   it answers; direct KVM calls remain the fallback. The device page shows what the hub learned
   about the wiring, with **Check again**. Reset and Force off appear while the LED is trusted and on.
8. **NanoKVM hardening guide** (`docs/nanokvm-hardening.md`): a power-only account on 2.5 for
   PowerDeck, password, firmware updates, Tailscale ACLs so only the hub reaches the KVM, services
   to turn off, and what the sealed credentials do and don't protect.

Test:
- Client against the fake: every login style and failure mode, re-login on 401, 403, HTTPS
  redirect and pin, a 12 s hold with the read timeout (scaled time).
- Hub actions against the fake with a fake helper: the right tool for each state, LED before and
  after, refuses to press when the LED is unreadable or untrusted, reset refused while the helper
  answers, force off then on, one action at a time, wiring learned and re-checked.
- Sealing: round trip, tampering detected, key file created 0600, wrong key fails.
- API: owner-only routes, no password in any reply, scopes for each action.
- Android: Robolectric tests against a fake hub (KVM link stored on the hub, actions through the
  hub, fallback to the direct KVM when the hub doesn't answer, migration of existing links).

## Phase 3: hang detection and events

Build: the hub watches each machine with a KVM. A machine is **hung** when, for N minutes (default
3): its helper misses heartbeats, `tailscale ping` fails, and the KVM reads the power LED on.
`off` = LED off; `on` = helper answers; `unknown` = not enough evidence. Extra evidence item:
"TCP accepts but nothing answers". State with evidence on the API; changes published as events
over SSE and to webhooks (HMAC-signed).

Test: detector table tests with a fake clock and fake helper/ping/KVM; SSE and webhook delivery and
retry; signature check.

## Phase 4: recovery with confirmation

Build: reset and force off as **propose → confirm → execute → verify**.
- Anyone with the right scope can propose (an agent key included). Only an owner device confirms,
  from a phone notification or an app card that shows the evidence.
- At most one recovery per machine per 30 minutes.
- Verify: heartbeat back within 5 minutes, else an alert event.
- Audit log: who proposed, who confirmed, what happened.
- Optional auto-confirm after M minutes, off by default, owner only.
- Waking a machine that's off can stay automatic.

Test: state machine tests (expiry, double confirm, agent can't confirm, rate limit, verify timeout);
app UI tests for the card; end to end against the fake KVM.

## Phase 5: assistant API, MCP, docs, hardware checklist

Build: versioned HTTP API and MCP tools `machines`, `status`, `wake`, `shutdown`, `restart`,
`propose_reset`, `propose_force_off`, `action_status`, `events`. Docs: `docs/protocol.md`,
`docs/integrating-an-assistant.md` (use a hub, or run your own copy) and
`docs/hardware-test.md` (wake from off, graceful restart, reset a deliberately hung PC, LED reads;
a memory-exhaustion hang in a VM first).

Test: MCP tool tests against the fake; doc examples run in CI.

## Out of scope

Working out why a machine hung. PowerDeck stays the power plane, with lightweight health signals only.
