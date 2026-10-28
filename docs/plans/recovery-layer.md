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
     doesn't answer, the reply says so and points to reset (no press). *Built:* `shutdown` with
     `press: true`, or for a machine without a helper, is a short power press (the OS's own clean
     shutdown), owner devices only and only while a trusted LED reads on, because the phone already
     offered that before the hub existed.
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
   to turn off, and what the sealed credentials do and don't protect. *Checked:* 2.5.1 and 2.5.2
   have only the `admin` and `user` roles, and `user` keeps keyboard, video and power; a grant of
   only `power` (which still shows the screen) exists on NanoKVM's main branch after 2.5.2, not in
   a release yet. The guide says both.

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

Today the hub asks each machine's helper and reads its KVM's power light every 2 minutes: `on` when
the helper answers, `off` when a trusted light reads off, `unknown` otherwise. Phase 3 tells a hung
machine apart from one that is booting, asleep, installing updates or just missing its helper, says
why, and tells whoever listens.

What can look like a hang and isn't (each one has its own rule below):
- **Booting** or restarting: helper silent and light on for a few minutes.
- **Installing Windows updates** during a restart: light on, network down, helper stopped, for up
  to an hour.
- **Asleep**: on most boards the light goes off or blinks, but on a Modern Standby PC (and some
  boards in S3) it stays on while the network sleeps.
- **The helper stopped** (crashed, uninstalled, updating) while the PC is fine: the network still
  answers.

Build:
1. **Watcher.** The 2-minute loop becomes a 30-second watcher for every machine with a KVM here. Each
   round collects evidence in parallel (8 s at most):
   - `helper`: answers its status (two tries), or silent.
   - `tcp`: the helper's port refuses (the OS is up and answered, the helper isn't running), accepts
     but never answers (what a hung Windows does: the kernel still accepts), or no reply.
   - `ping`: `tailscale ping --icmp -c 2 --timeout 3s --until-direct=false <Tailscale IP>`. ICMP
     goes through WireGuard to the machine's own network stack, so it needs more of the machine
     alive than a disco or TSMP ping (both answered by tailscaled alone). *Checked:* a Windows 11 PC
     with its default firewall answers it. Replies, no reply, or not available (the hub isn't on
     Tailscale, or the machine has no Tailscale address).
   - `led`: three readings 0.5 s apart: on, off, blinking (mixed), or unreadable; and whether the
     light is trusted (phase 2's wiring).
   - `tailnet`: whether Tailscale's control plane lists the node online. Shown, never decides: a
     hung PC stays "online" there for minutes.
   - The helper's last word: its last status (Windows updates pending, a shutdown or restart
     counting down) and its notes (next item).
   Wiring is learned from the same readings: a light seen on while the machine runs is wired; seen
   off while its helper answers counts towards "not wired" at most once every 30 s.
2. **Notes from a helper to its hubs.** When its machine is about to sleep, shut down or restart, and
   when it wakes, a helper tells every helper it holds a peer key for, all at once, best effort
   (1.5 s): `POST /v2/note {"event": "sleep|shutdown|restart|resume", "updates": true}`. Peer keys
   only; the hub works out the machine from the caller's address, so a helper speaks only for its
   own. Windows: suspend and resume through `PowerRegisterSuspendResumeNotification` (a callback
   Windows waits for, unlike a service's power events, which are answered as soon as they're
   queued), and the service's pre-shutdown notice (Windows waits for that too); a shutdown is sent
   as `restart` when PowerDeck's own restart is counting down, with whether updates are pending.
   Linux: `shutdown` when systemd is stopping the whole system. Not relied on: Windows may not send
   the suspend event on Modern Standby PCs, so item 3 covers sleep without it.
3. **Sleep facts.** A Windows helper's status gains
   `"sleep": {"s3": true, "modernStandby": false, "afterMinutes": 30}` from `GetPwrCapabilities`
   and the active power plan's sleep timeout on AC (`PowerReadACValueIndex`, 0 = never); no
   `powercfg` text, which is translated. The hub keeps the last one it saw. The first `sleep` note
   teaches the hub what the light does while that machine sleeps (read 30 s after the note): stored
   as `wiring.ledAsleep` = off, blinking or on.
4. **Detector**, a pure function of the evidence history (table-tested):
   - `on`: the helper answers.
   - `off`: a trusted light reads off. Blinking, or off after a `sleep` note: `off` with
     `asleep: true`.
   - `hung`: for `hungAfter` (default 3 minutes; owner setting, 2 to 30), every round saw the helper
     silent, the port not refusing, ping not replying (or not available), and a trusted light
     steadily on; and none of these hold:
     - booting: 10 minutes after the hub woke, restarted, reset or forced off the machine, or after
       its light came on;
     - a shutdown or restart was coming (a note, or a countdown in its last `hello`): 10 minutes,
       or 90 minutes when Windows updates were pending (they install with the light on and the
       network down);
     - it may be asleep with the light on: a `sleep` note since it last answered, or it can sleep
       on its own (`afterMinutes` > 0) and its light while asleep is on or not learned yet.
     (These carry the reasons `booting`, `shutting_down`, `restarting`, `updating` and
     `may_be_asleep`.)
   - Anything that contradicts a hang (helper answers, ping replies, port refuses, light off or
     blinking) starts the count again. An unreadable light neither counts nor resets.
   - `unknown`: everything else, with a `reason`: `no_helper`, `led_not_trusted`,
     `led_unreadable`, `booting`, `shutting_down`, `restarting`, `updating`, `may_be_asleep`,
     `helper_stopped` (silent while the network answers), `checking` (silent for less than
     `hungAfter`).
   A machine without a helper, or without a trusted light, is never `hung`.
   `GET /v2/machines` and each machine gain `since` (when the state began), `reason`, and
   `evidence` (the last round: each item with its value and time).
5. **Events.** Each state change is an event: `{"id", "at", "machine", "state", "from", "reason",
   "evidence"}`, plus `recovered` when a hung machine answers again. The last 500 are kept in
   memory; `hung` and `recovered` also go to the activity log.
   - `GET /v2/machines/events` (status scope): Server-Sent Events. Starts with a `snapshot` of every
     machine; with `Last-Event-ID`, replays what came after it instead (a snapshot when that's no
     longer held). A comment every 25 s keeps proxies open. The server's 150 s write timeout is
     lifted for this stream only, with a 10 s deadline per write: a client that stops reading is
     dropped. At most 16 streams.
   - **Webhooks** (owner devices): `GET/POST /v2/webhooks`, `DELETE /v2/webhooks/{id}`,
     `POST /v2/webhooks/{id}/test`. The URL is https, or http to a Tailscale or loopback address
     (WireGuard already encrypts those). Event filter (default `hung`, `recovered`). The secret is
     shown once when it's created and sealed at rest like KVM passwords. Each delivery is the event
     as JSON with `X-PowerDeck-Event`, `X-PowerDeck-Delivery` (the event id),
     `X-PowerDeck-Timestamp` and `X-PowerDeck-Signature: sha256=<hex HMAC-SHA256(secret,
     timestamp + "." + body)>`. Any 2xx is delivered; otherwise retried after 10 s, 1 min and
     5 min, then dropped and logged. 10 s timeout, redirects not followed. The list shows each
     hook's last result.
   - `docs/protocol.md` gains "Machine states and events", with how to check a signature.
6. **App:** a hung machine shows as hung on its device page, with the evidence and since when. The
   recovery card comes in phase 4.

Test:
- Detector table tests with a fake clock: hung after `hungAfter` and not before; each
  contradiction starts the count again; an unreadable light holds it; boot grace; shutdown and
  restart notes; 90 minutes with updates; Modern Standby and a light that stays on in sleep;
  blinking; no helper or untrusted light is never hung; recovered.
- Watcher end to end against the fake NanoKVM, a fake helper, a fake pinger, and real TCP
  listeners: one that accepts and never answers, one that's closed.
- Notes: accepted from the machine's own peer key only.
- Sleep facts on the Windows CI runner (values logged); the power-event handler's mapping in a
  unit test (a CI runner can't be put to sleep: the hardware checklist covers a real one).
- SSE: snapshot first, events in order, `Last-Event-ID` replay, heartbeat, stream limit, a client
  that stops reading is dropped.
- Webhooks: signature checked by an independent implementation, retries on a fake clock, URL
  rules, the secret never in a reply and sealed at rest, owner devices only.
- Android: Robolectric test for the hung state and its evidence on the device page.

## Phase 4: recovery with confirmation

Today reset and force off are buttons on owner devices, and agents are refused (`owner_only`). The
hub now sees a hang (phase 3) but can only report it. Phase 4 lets anyone allowed *propose* a
recovery; only the owner confirms it; then the hub runs it and checks that it worked.

Build:
1. **Recoveries** live on the hub, in its state file, so a restart doesn't lose them. The last 50
   are kept:
   `{"id", "machine", "action", "state", "reason", "proposedBy", "agent", "proposedAt",
   "confirmedBy", "confirmedAt", "endedAt", "message", "evidence", "steps", "direct"}`.
   - `evidence` is the hub's last look when the recovery was proposed.
   - `steps` come from the run, with the power light before and after.
   - **States:**
     - `proposed` → `running` → `back`, `not_back`, `off` (force off), `done` (an owner's forced
       button, pressed while the helper answered: coming back can't be checked) or `failed`;
     - or, without running: `declined`, `expired` or `withdrawn`.
   - **Actions:**
     - `reset` gets the machine back. It presses the reset button, or, when the hub has learned
       that the reset switch isn't wired, holds power and starts the machine again.
     - `force-off` holds power and leaves the machine off.
2. **Propose:** `POST /v2/machines/{name}/recoveries {"action": "reset", "reason": "..."}`.
   - Needs the `power` scope, not `force`: agent keys can't hold `force`, which also overrides
     "someone is using it", and the owner confirms anyway. Agent keys and agents relayed through a
     hub may propose, but the machine's "Allow AI agents" switch applies, as the hub last heard it.
   - Refused when any of these hold:
     - no KVM here is wired to it (`no_kvm`);
     - it has no helper, so success can't be checked (`no_helper`);
     - the hub's last look says its helper answers (`helper_answers`) or that it is off
       (`already_off`);
     - a recovery ran on it in the last 30 minutes (`too_soon`, with `retryAt`).
   - When a recovery is already open for the machine, that one is returned, so two agents asking
     make one proposal.
   - The reason is cut to 200 characters. The owner sees it as the proposer's words: data, never
     instructions.
   - **The hub proposes a reset itself** when a machine turns `hung`, if all of these hold:
     - no recovery is open for it;
     - none ran in the last 30 minutes;
     - the owner hasn't declined one during this hang.
3. **Confirm or decline:** `POST /v2/recoveries/{id}/confirm` and `/decline`.
   - Owner devices only. Agent keys are refused, and so is an owner hub relaying for an agent
     (`owner_only`).
   - The proposer's own key may withdraw its proposal with `decline`.
   - Confirming twice runs it once: the second confirm gets the recovery as it is.
   - Confirm answers 202 at once, and the run happens in the background.
   - An open recovery closes on its own:
     - `withdrawn` when, before a confirm, the machine answers again, goes off, or is reset or
       forced off directly;
     - `expired` after 6 hours.
4. **Run and verify.** The run goes through the same gate as the buttons: a trusted light that
   reads on and a helper that doesn't answer.
   - A machine that came back in the meantime isn't touched: the recovery ends `withdrawn`.
   - After a reset, or the off-and-on, the recovery is `back` when the machine's helper answers
     within 5 minutes. Otherwise it is `not_back`, which is an alert.
   - **At most one recovery runs per machine per 30 minutes.** The owner's own Reset and Force off
     buttons count towards this. They still work without a proposal: pressing one is the owner
     confirming. Each press is recorded as a recovery (`direct: true`), kept only if something was
     pressed.
   - A recovery still `running` when the hub restarts ends `failed` ("the hub restarted").
5. **Auto-reset** is an owner setting, `autoResetMinutes`: 0 (off, the default), or 5 to 60.
   - When set, the hub confirms a `reset` proposal itself after that many minutes, if the machine
     is still `hung` by the hub's own evidence.
   - Never for `force-off`.
6. **Events and audit.**
   - Each change of a recovery's state is an event: `{"event": "recovery", "machine", "recovery":
     {...}}`.
   - On the event stream it's the SSE event `recovery`.
   - Webhooks get it with the event filter `recovery`, which new webhooks have by default.
   - The activity log gets one line for each step: who proposed and why, who confirmed or declined,
     and whether the machine answered again.
   - `GET /v2/recoveries` (status scope; `?open=1` for open ones only) and
     `GET /v2/recoveries/{id}`. Both show only machines the caller may see.
7. **App:**
   - **Device page:** while a recovery is open, a card shows:
     - "pve suggests a reset", or "Atlas asks to reset";
     - what pve sees, and the proposer's reason;
     - **Reset** and **Not now** buttons.

     While it runs: "Resetting…, waiting up to 5 minutes for it to answer". After: "Back at 14:23",
     or "Didn't come back", with Force off.
   - **Notification:** "gaming-pc looks frozen. pve can reset it."
     - Its **Reset** action works only on an unlocked phone (`setAuthenticationRequired`).
     - Tapping it opens the device page.
     - "Didn't come back" is notified too.
     - For a machine with a reset proposal, this replaces the plain "looks frozen" notification.
   - **The hub's own page → Frozen machines:** "Reset a frozen machine on its own": Off, or after
     10, 30 or 60 minutes. (It's the hub's setting, so it sits with the hub, shown once it drives a
     KVM; Settings → KVM hub only picks which helper is the hub.)

Test:
- State machine tests with a fake clock:
  - every propose refusal, and an open recovery returned instead of a second one;
  - confirm by owner devices only: agent key, owner hub relaying for an agent, and the proposer's
    key are all refused;
  - a double confirm runs once;
  - decline, and withdrawal when the machine answers or goes off;
  - expiry at 6 hours;
  - the 30-minute limit, counting the direct buttons;
  - auto-reset after M minutes only while hung, and never for force off;
  - a confirm after the machine came back presses nothing.
- End to end against the fake NanoKVM and a fake helper:
  - hang → hub proposal → confirm → reset → `back`;
  - a reset that doesn't bring it back → `not_back` event and log line;
  - reset switch not wired → off and on.
- A hub restart during a run ends it `failed`.
- Server: routes, scopes, recovery events on SSE and webhooks.
- Android:
  - card states (Robolectric screenshot);
  - parsing recoveries;
  - the notification's Reset action needs an unlocked phone and confirms;
  - no duplicate "looks frozen".

## Phase 5: assistant API, MCP, docs, hardware checklist

The HTTP API is already versioned (`/v2`) and phases 2–4 added the hub's routes to it. Phase 5 makes
the same things reachable over MCP, writes down how an assistant should use them, and gives the
owner a checklist to try them on the real machines.

Build:
1. **MCP tools.** The five tools agents use today keep their names, so existing configs keep
   working:
   - `list_devices`, `get_status`, `power_on`, `power_off` (shutdown or restart) and `cancel`.

   On the hub (a helper with a KVM linked) these are added. Other helpers don't list them, though
   they still answer (a KVM linked during a session doesn't take tools away from an agent):
   - `list_machines` (read-only): the machines with a KVM here that the key may use, each with its
     state (`on`, `off`, `hung`, `unknown`), `reason`, `since`, the hub's `evidence`, `actions` and
     open `recovery`. This is the plan's `machines`.
   - `propose_reset` and `propose_force_off`: take `{machine, reason}` and return the recovery.
     They need the key's `power` permission, and the owner confirms on the phone. Asking again while
     one is open returns that one.
   - `get_recovery` (read-only): takes `{id}` and returns where the recovery is now. This is the
     plan's `action_status`: wake, shutdown and restart already answer with their result, so only
     a recovery has a status to come back for.
   - `withdraw_recovery`: takes `{id}`. The proposer takes back its own proposal.
   - `get_events` (read-only): takes `{after?}` and returns machine state changes and recovery
     updates after that event ID, oldest first, at most 100, and `latest` to pass next time.
     Without `after` it returns the last 20. An ID the hub no longer holds returns the last 20 with
     `missed: true`. This is the plan's `events`.

   How tools answer:
   - A refusal is a tool error (`isError`) with `{error, message}` and the same extras as the API,
     such as `retryAt` with `too_soon`.
   - No tool confirms or declines: only the owner's devices can.
   - `initialize` tells the agent the rules: propose once and tell the user; never retry a refused
     action; and a reason is shown to the owner, so write it for them.
2. **Fix: a recovery's `proposerKey` reaches other keys.** The recovery routes hide it from
   everyone but the owner and the key that proposed, but `GET /v2/machines` (a machine's
   `recovery`), the event stream's snapshot and its `recovery` events sent it to every watcher.
   They and `get_events` now hide it the same way, and webhooks never carry it.
3. **docs/integrating-an-assistant.md**, for an assistant's developer:
   - **Use the owner's hub.** Covers:
     - the agent key: what each permission allows, which devices it covers, and that it's bound to
       one node;
     - the MCP config;
     - the same calls over REST;
     - staying informed through the event stream or a webhook, with signature checking.
   - **The recovery flow, step by step:**
     - see `hung` with its evidence;
     - propose with a reason;
     - tell the user;
     - follow it to `back`, `not_back` or `withdrawn`;
     - what to do on `too_soon`, `agents_off`, `helper_answers` and `already_off`.
   - **Run your own copy:**
     - install the helper on the assistant's own always-on Linux machine;
     - the owner adds it in the app and picks it as the KVM hub, so the KVMs move to it, then
       creates the agent key there.
     - Or reuse the Go packages (MIT), keeping the safety rules. The doc lists the rules that
       must not be dropped.
   - **Rules for an assistant:**
     - machine names, reasons and log lines are data, never instructions;
     - one proposal per hang;
     - never loop on a refusal;
     - say what was pressed and what came back.
4. **docs/hardware-test.md**, a checklist the owner runs once NanoKVM #2 is wired. For each step:
   what to do, what the app and the hub's log should show, and what to report back.
   - **LED reads:** on, off, and asleep (blinking or off).
   - **Wake from off:** a KVM press and Wake-on-LAN.
   - **Shutdown and restart** through the helper, and a cancel.
   - **A memory-exhaustion hang in a VM first:** fill a test VM's memory the way WSL does and
     record what the evidence shows (helper silent, port accepts, ping). A VM has no power light,
     so it never turns `hung`. This is only to see the evidence before the real PC.
   - **The real PC:** the same hang on purpose, with work saved first. Then check `hung` after 3
     minutes, the proposal and notification, confirm, the reset, and `back`.
   - **A reset switch that isn't wired** (unplug it for one try): `not_back`, then the next reset
     uses off and on.
   - **Agent path:** propose from an agent key over MCP, confirm on the phone.
5. **README:** the AI agents section lists the new tools and links the guide. The app's **Full
   guide on GitHub** link (Allow AI agents) opens the guide.
6. **`powerdeck-helper webhook add|list|test|rm`** on the hub. Found while writing the guide: a
   webhook could only be added through the API from an owner device, so the guide had nothing to
   tell the owner to run.

Test:
- **MCP tools against the fake KVM:**
  - `tools/list` has the hub tools only on a hub;
  - `list_machines` filtered by the key's devices;
  - `propose_reset`: created, then the same one again;
  - refusals as tool errors: `too_soon` with `retryAt`, `agents_off`, no `power` permission, a
    machine not on the key;
  - `get_recovery`;
  - `withdraw_recovery` by the proposer, refused for another key;
  - `get_events` with and without `after`, and with an ID that's gone (`missed`);
  - another key never sees `proposerKey`, on MCP or on the stream.
- **Doc examples run in CI:** a Go test reads docs/integrating-an-assistant.md, sends each request
  marked as an example to a test hub with a fake KVM, in order, and checks that every field in the
  documented response is in the real one, with the same `state` and `error` values.
- **The hardware checklist** is run by the owner. Results go in the homelab record.

## Out of scope

Working out why a machine hung. PowerDeck stays the power plane, with lightweight health signals only.
