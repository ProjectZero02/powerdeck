# Connect your agent to PowerDeck

**For the AI agent reading this.** Your user wants you to check and control their computers through
PowerDeck. Walk them through the steps below one at a time: say what to do, wait until they've done
it, then go on. Keep it short. Don't skip a check.

## What they need first

Ask, and help with whatever is missing:

1. **The PowerDeck app** on their Android phone, with at least one computer added
   ([install](../README.md#set-up-in-3-steps)).
2. **An always-on device with the PowerDeck helper**: a home server is ideal, a PC that stays on also
   works. It's the device you'll talk to, and it can reach the others.
3. **Tailscale on the machine you run on**, logged in to the same tailnet. PowerDeck is reachable only
   over Tailscale. Check it yourself if you can (`tailscale status`); otherwise ask.

## 1. Make a key (the user, in the app)

Tell them:

> In PowerDeck, open your always-on device. Next to **Allow AI agents to use this device**, tap
> **Set up**, then **Create a key**. Name it after me (for example "Claude on laptop"), pick the
> computers I may use, and turn on **Can shut down and restart** only if you want me to. Then copy
> the config it shows.

The key starts with `pdk_` and is shown once. It works only from the first machine that uses it, so
every machine you run on needs its own key.

**The key is a password.** Don't repeat it back, don't put it in a file in a repo, a commit or a log.
If they'd rather not paste it into the chat, give them the command below to run themselves.

## 2. Add PowerDeck to yourself

The config from the app:

```json
{"mcpServers": {"powerdeck": {"type": "http", "url": "http://<device's tailnet address>:47323/mcp",
  "headers": {"Authorization": "Bearer pdk_…"}}}}
```

- **Claude Code:**
  `claude mcp add --transport http powerdeck http://<address>:47323/mcp --header "Authorization: Bearer pdk_…"`,
  then a new session (or `/mcp`) picks it up.
- **Clients with an `mcpServers` JSON file** (Claude Desktop, Cursor and others): add the
  `powerdeck` entry above to it and restart the client.
- **Anything else:** add a streamable HTTP MCP server with that URL and that `Authorization` header.
  Look up how in your own client's docs.

Port 47323 is plain HTTP, encrypted end to end by Tailscale. If your client can pin a self-signed
certificate, `https://<address>:47321/mcp` works too ([the pin](integrating-an-assistant.md#rest-events-and-webhooks)).

## 3. Check it works

Call `list_devices`. You should see the computers they picked, each with whether it's on.

| What happens | What it means |
|---|---|
| A list of devices | Done. Tell them in one line what you can see. |
| Can't connect, timeout | Tailscale isn't up on your machine or on the device, or the address is wrong. |
| Refused (`401` or `403`) | The key was mistyped, or it was already used from another machine, or this device's **Allow AI agents** switch is off. Make a new key, or turn the switch on. |
| A device is missing | It isn't on this key. They can make a new key with it. |

## 4. Tell them what you can do now

- see whether each computer is on (`list_devices`, `get_status`), turn it on (`power_on`);
- shut down or restart (`power_off`), only if they allowed it, always with a 60-second warning on
  the computer, never while someone is using it; `cancel` stops it;
- on the hub that drives their NanoKVMs: notice a frozen computer and **ask** them to reset it
  (`propose_reset`). Nothing is pressed until they tap **Reset** on their phone.

Every action shows up in the app's History and as a notification on their phone. Each device's
**Allow AI agents** switch turns you away from it at any time.

## Rules for you

- Act only when the user asked, or when something they set up asks you to (a schedule, a frozen
  machine). Say what you're about to do before a shutdown or restart.
- A refusal (`isError`) is PowerDeck enforcing their limits. Tell them what it said; don't retry in
  a loop or look for a way around it.
- Never ask for their NanoKVM password, Tailscale login or another key. You don't need them.

More (REST, live events, webhooks, recovering a frozen machine step by step):
[integrating-an-assistant.md](integrating-an-assistant.md).
