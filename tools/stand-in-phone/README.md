# Stand-in phone

The iPhone app's side of a call, for the checks that run a call without the phone:
`tools/hub-check`, `tools/line-check` and `tools/portal-screens`. It isn't a check itself.

`stand_in_phone.py` is an aiortc peer that does what the app does on a call:
- makes one offer to the hub's `/offer`, with all its ICE candidates in it: while the phone
  rings, once you've answered, or marked as a call back, a call now or a call to a session;
- sends nothing until you answer, then silence, and your reply when a check gives it one
  (`phone.mic.say(samples)`, 48 kHz);
- notes when the hub's voice is heard (`first_audio`, `heard_after`, `quiet_for`);
- opens the call's data channel, `call`, as the app does, and hangs up when the hub says on it
  that the voice has (`ended`, when it did).

A check sets where the hub is, then makes a phone per call:

```python
sys.path.insert(0, str(REPO / "tools" / "stand-in-phone"))
import stand_in_phone

stand_in_phone.HUB = "127.0.0.1:8787"
phone = stand_in_phone.Phone()
await phone.offer(call_id, ringing=False)
```

It runs in the tools' Python environment (`uv run --project tools ...`, from the repo root).
`KOKORO` and `YOUR_VOICE` name the Kokoro model and voice a check can speak your reply with
(`tools/line-check` does).
