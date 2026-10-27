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
- Go NanoKVM client in the helper (both login variants, cookie session, HTTPS-redirect handling,
  403 as a permission error, timeouts longer than the press).
- A **fake NanoKVM** (Go, used by unit and integration tests; also runnable for app development):
  login variants and lockout, power LED that follows presses with a delay, reset, a 12 s hold that
  forces off, Wake-on-LAN recording, "unwired LED" and "KVM unreachable" modes.
- Machine map on the hub: machine → helper, KVM (address, model, credentials), MAC, LAN.
  Credentials are encrypted at rest: DPAPI on Windows; on Linux AES-GCM with a key in a separate
  root-only file. The docs say what that does and doesn't protect against.
- Power actions with the right tool for each state, the LED read before and after, never blind:
  - wake: KVM power press when the LED reads off, otherwise Wake-on-LAN from a helper or KVM on that LAN;
  - shutdown and restart: through the machine's helper (60 s countdown);
  - reset: the KVM reset switch;
  - force off: 12 s hold, then power on.
- **Wiring doesn't need configuring.** The hub works out what's connected:
  - LED: compared with the helper. On while the helper answers means it's wired. Always off
    while the helper answers means not wired or reversed, and the hub then won't trust it for "off".
  - Power switch: learned when a press wakes the machine.
  - Reset switch: learned on the first confirmed reset. If the machine doesn't drop and come back
    within about 20 s, the reset counts as not wired, and recovery moves on to the 12 s hold and a
    power-on.
  What was learned is shown with the machine and can be re-checked.
- The app uses the hub for KVM actions when one is set up; direct KVM calls remain as a fallback.
- NanoKVM hardening guide.

Test: client against the fake (every login and failure mode); action selection for each state; refuses
to press when the LED can't be read; Android against the fake through the hub.

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
