# Hardware test

The automated tests use a fake NanoKVM. This checklist is for the real thing. Run it once after
wiring a NanoKVM to a computer, and again after a NanoKVM firmware update or any rewiring. It takes
about an hour.

**Save your work on the computer first.** Steps 7 to 10 cut its power.

Each step says what to do and what you should see. When something doesn't match, note the step,
what the app said, and the hub's **History** (the hub's page in the app), and report it. Don't work
around it.

## Before you start

- [ ] The phone app, the hub and the computer run PowerDeck 2.5 or later.
- [ ] The NanoKVM's ATX board is wired to the computer's front-panel header: power switch, reset
      switch and power LED. The LED pins are polarised; your motherboard manual shows the header.
- [ ] Write down the motherboard model, and whether the case has its own reset button. Both
      matter if a step fails.
- [ ] The NanoKVM has its own account for PowerDeck, not `admin`
      ([NanoKVM hardening](nanokvm-hardening.md)).
- [ ] In the app, the computer's page → **Link a NanoKVM** is done, and the computer's helper
      answers.

Most steps are read from the computer's page in the app. **Power button** shows the KVM and
what the hub has learned about each lead (power light, power button, reset button). While the
computer's helper is silent, **What *hub* sees** shows whether PowerDeck on it answers, the
network, and the power light.

The page shows changes within seconds. Notifications can take up to about 15 minutes, because
Android lets the app check in the background only that often.

## A. The power light

| # | Do | Expect | ✓ |
|---|---|---|---|
| 1 | With the computer on, wait a minute and open its page | **Power light: Wired** | |
| 2 | Sleep the computer (Start → Power → Sleep), wait 2 minutes, open its page | Asleep, or **May be asleep** if your board keeps the light on while asleep. Never frozen | |
| 3 | Wake it from the app | It answers again within about a minute | |

If step 1 says the light reads off while the computer runs, the LED lead is unplugged or the wrong
way round. Turn the plug round. Nothing is pressed until the light has read on once.

## B. Off and on

| # | Do | Expect | ✓ |
|---|---|---|---|
| 4 | **Shut down** from the app | A 60-second warning on the computer, then off. The light reads off within a minute | |
| 5 | **Turn on** from the app | The hub presses power. The light comes on and the helper answers within about 90 seconds. **Power button: Works** | |
| 6 | **Restart**, then **Cancel** within the 60 seconds | The warning goes away and nothing happens | |
| 6b | Unplug the KVM's power lead, shut down from the app, and **Turn on** | The press does nothing, so the hub sends Wake-on-LAN instead and the computer starts. **Power button: A press did nothing** | |
| 6c | Plug the power lead back in and tap **Check again** under **Power button** | Every lead is back to being checked; nothing is pressed | |

Step 6b needs Wake-on-LAN on in the BIOS. On many boards that's *ErP Ready: Disabled* and *Power
On By PCI-E: Enabled*. If it doesn't wake, PowerDeck still works through the power button, but
note it.

## C. The memory test, in a VM first

This test shows what a freeze from running out of memory looks like before you freeze the real
computer. Use a VM with the PowerDeck helper installed and no swap, or one with little swap.

The memory hog keeps taking 256 MB more and writes to all of it, until the system gives
out. Stop it with Ctrl+C while you still can:

```sh
python3 -c "a=[]; [a.append(b'x'*(256<<20)) for _ in iter(int, 1)]"
```

| # | Do | Expect | ✓ |
|---|---|---|---|
| C1 | In the VM, use up all memory: run the memory hog below, or open things until it stalls | In the app, the VM's helper stops answering within a few minutes. Note how long it took, and whether the app's status for it ever answers slowly instead | |
| C2 | Stop the memory hog (or reset the VM from its host) | The helper answers again. Note how long it took | |

The VM has no NanoKVM, so the hub can't call it frozen. This step is only about whether a
memory-starved machine stops answering at all.

## D. A real freeze

A common freeze is running out of memory, for example in WSL. This reproduces one on purpose. If
your computer froze some other way, use that instead.

| # | Do | Expect | ✓ |
|---|---|---|---|
| 7 | Save your work. In WSL, run the memory hog until Windows stops responding. WSL is capped at half the PC's memory unless `.wslconfig` raises its `memory=` limit, so you may need to raise it first | The computer's page says **Not answering**, then, after 3 minutes, **Frozen since** *time*, with "*hub* suggests a reset" and **Reset** and **Not now**. The same comes as a notification | |
| 8 | Before answering, write down **What *hub* sees** | PowerDeck on it: not answering. Network: no reply to ping. Power light: on | |
| 9 | Tap **Reset** (on the page, or in the notification on an unlocked phone) | "Resetting…", then within 5 minutes "*computer* is back". **Reset button: Works**. The hub's History: `confirm reset`, then `answers again after the reset` | |

**If step 7 never says frozen**, step 8 says why. If the network answers ping while the computer is
frozen, the page says **Running, but PowerDeck on it isn't answering** and the hub won't suggest a
reset; you can still press **Reset** on its page. Report what step 8 showed: the hub's rules are
built on it.

## E. A reset that doesn't work

| # | Do | Expect | ✓ |
|---|---|---|---|
| 10 | Unplug the KVM's reset lead. Freeze the computer again (step 7) until its page says frozen, then press **Reset** on its page | After 5 minutes: "*computer* didn't come back", with **Force off**. A notification follows on the alerting **Frozen machines** channel | |
| 11 | Tap **Force off** | The hub holds power for 12 seconds and the light goes off. Turn it on from the app | |
| 12 | Plug the reset lead back in and tap **Check again** under **Power button** | Every lead is back to being checked; nothing is pressed | |

The hub doesn't suggest a reset within 30 minutes of the last one, so step 10 uses the page's own
**Reset** button. Your own buttons are never held back.

## F. An assistant

Start at least 30 minutes after section E. You need an agent key for the computer that **Can shut
down and restart**, as in [integrating-an-assistant.md](integrating-an-assistant.md).

| # | Do | Expect | ✓ |
|---|---|---|---|
| 13 | Have the assistant call `list_machines` while the computer runs | The computer is `on` | |
| 14 | Have it call `propose_reset` while the computer runs | Refused: `helper_answers` | |
| 15 | Freeze the computer (step 7). Once its page says frozen, have the assistant call `propose_reset` | `created: false`: it gets the hub's own request back. Nothing is pressed | |
| 16 | Tap **Not now** | The assistant sees `declined` with `get_recovery`. Nothing is pressed | |
| 17 | Have it call `propose_reset` again, confirm in the app, and follow it with `get_recovery` | `running`, then `back` | |
| 18 | Turn off the computer's **Allow AI agents** switch and have it propose again | Refused: `agents_off` | |

Turn the switch back on afterwards if you want the assistant to keep its access.

## When you're done

- [ ] Every row is ticked, or reported with what the app and History said.
- [ ] Automatic resets (the hub's page → **Frozen machines**) are left as you want them. They're off
      unless you turn them on.
