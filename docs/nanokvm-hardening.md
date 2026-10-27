# Hardening a NanoKVM for PowerDeck

Whoever can log in to a NanoKVM can press that computer's power and reset buttons, and on most
firmware can also see its screen and type on it. PowerDeck keeps the password safe in its own
storage, but how much a stolen password is worth depends on the NanoKVM. This page covers
firmware 2.5.x of the NanoKVM (Cube, Full, Lite and PCIe). The NanoKVM Pro has its own firmware and
settings.

## 1. Give PowerDeck its own account

PowerDeck uses four things on the NanoKVM: logging in, reading the power light and pressing the
buttons (`/api/vm/gpio`), Wake-on-LAN (`/api/network/wol`) and the device info
(`/api/vm/info` and `/api/vm/hardware`, for the firmware version). None of them needs an administrator.

**Firmware 2.5.1 and 2.5.2:** these added user accounts with two roles. Under **Settings → Users**,
add an account (for example `powerdeck`) with the role **User** and a long random password, and
give PowerDeck that account instead of `admin`.

A **User** can't open the terminal, run scripts, change the network, SSH, Tailscale or MCP settings,
install updates or manage accounts. It keeps the video, keyboard, mouse, paste, Wake-on-LAN and power
controls, so its password is still console access to that computer. Treat it like one.

**Newer firmware with permissions:** the NanoKVM's development branch (after 2.5.2, not released
when this was written) replaces the fixed User role with grants. Give the PowerDeck account only
**Power**. That includes watching the screen but not typing on it. If an action then fails with
"isn't allowed to use the power controls" (`kvm_forbidden`), the account lacks the Power grant.

**Firmware before 2.5.1** has only the one `admin` account. Use a long random password, and consider
updating.

To change the account or password PowerDeck uses, open the KVM in the app and link it again. The
app sends the new password to the hub, which tries it before storing it.

## 2. Reach it only through Tailscale

PowerDeck only talks to a NanoKVM by its Tailscale name or address, never by its LAN address,
because the NanoKVM's login hides the password with a key that's public (see
[protocol.md](protocol.md#phone--nanokvm)). Tailscale's encryption is what actually protects it.

- **Tailscale access rules (ACLs).** Only your own devices (for the app) and the hub (the PowerDeck
  device that drives it, e.g. your home server) need to reach the NanoKVM. With the NanoKVM and the
  hub tagged, rules like these allow exactly that:

  ```json
  {
    "grants": [
      { "src": ["autogroup:member"], "dst": ["tag:kvm"], "ip": ["tcp:80", "tcp:443"] },
      { "src": ["tag:server"], "dst": ["tag:kvm"], "ip": ["tcp:80", "tcp:443"] }
    ]
  }
  ```

  Adjust the sources to your own tags and users. With the default "allow all" policy, every device
  on your tailnet can reach its login page.
- **The LAN.** The NanoKVM is also on your home network. Anyone there can try its password, so it
  should be a long random one (the NanoKVM locks logins for a while after repeated failures). If your
  router can, put it on a network your guests' devices aren't on.

## 3. Turn off what you don't use

Under **Settings** (as `admin`):

- **SSH**: off, unless you use it. If you keep it, change the root password (the SSH login
  password), which is separate from the web accounts.
- **MCP Service**: off, unless an AI agent should see the screen and type. PowerDeck doesn't use
  it; PowerDeck's own MCP tools go through the hub and its rules.
- **mDNS**: off. PowerDeck finds the NanoKVM through Tailscale.
- **Updates**: keep the firmware current, from the NanoKVM's own update page.

HTTPS on the NanoKVM is optional. Tailscale already encrypts the connection. If you turn it on, the
hub remembers the NanoKVM's certificate the first time and refuses a different one later
(`kvm_cert_changed`: link it again if you reset the NanoKVM).

## What PowerDeck protects, and what it doesn't

| Where | How the password is kept |
|---|---|
| Your phone | Encrypted with a key in Android's hardware keystore. App backups are off |
| The hub, Linux | Sealed with AES-256-GCM. The key is `/etc/powerdeck/secret.key` (root only), kept apart from the data in `/var/lib/powerdeck` |
| The hub, Windows | Sealed with DPAPI for the service account (only SYSTEM can open it) |
| On the way | Phone to hub: TLS with the hub's pinned certificate. Hub to NanoKVM: Tailscale (and HTTPS if it's on) |

Sealing means a copy or backup of the hub's data folder doesn't give the password away. It doesn't
protect against someone who is root (or an administrator) on the hub: they can read the key, or
act as the helper. The hub can't be the computer its own KVM is wired to, so a frozen hub never
needs its own KVM.

The rules PowerDeck applies before pressing anything are in
[protocol.md](protocol.md#kvms-on-the-hub). If you think the password leaked: change it on the
NanoKVM, then link the KVM again in the app.
