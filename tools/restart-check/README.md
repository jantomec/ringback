# Restart check

Checks that the voice server loses nothing that matters when it restarts ("Staying up" in
`docs/design.md`). It keeps what sessions and their calls need in `voice-state.json`, beside
its socket, written on every change and read at start. A voice server is started here with
its own socket, state file and temporary folder, and phone-check's ports 18797 and 18798, so
it runs beside your own voice server, but not beside `tools/phone-check` or `tools/ask-check`.
It's killed twice, once with SIGTERM and once with SIGKILL, and started again each time. A
stand-in switchboard holds the calls of the project "held" for an hour, as outside your work
hours, and takes the others, so nothing rings. The phone's side is phone-check's: aiortc, and
your words spoken by Kokoro.

Before the first restart, through the session socket as the mod sends them: a permission
places a held call; a briefing places a call nobody answers, and a permission another; a call
is answered, and a permission is put to it. A long poll waits for the unanswered call's
permission. Then it checks:
- killed with SIGTERM: the long poll fails, as a mod's would, the killed server's Haiku
  processes end, and the state file can be read only by you;
- started again: the held call is still held, a long poll says so, an offer for it is
  refused, and it asks the switchboard again with the same call ID. A long poll for the
  unanswered call's permission carries on under the same id. The answered call has ended: its
  session is told it "was answered, and ended when the voice server restarted", with its
  transcript, and its permission still waits, as a missed call's does;
- a message is left for a session, and the voice server killed with SIGKILL at once. Started
  again: the held call is still held and asks the switchboard again; the message and both
  missed calls' outcomes wait for their sessions;
- the missed call is called back, and the voice opens with what it was about; the call placed
  to ask a permission is called back, the voice asks it, and your spoken "Yes, go ahead."
  allows it; the answered call that ended is called back, the permission it was asked goes to
  it, and your yes allows it.

It also counts the Claude Code session folders the killed servers' Haikus left in
`~/.claude/projects` (a risk in `docs/design.md`), and removes them.

Run from the repo root. It uses your Claude subscription, four Haiku sessions:

    uv run --project voice tools/restart-check/restart_check.py [--unanswered S] [--reask S] [--keep DIR]

- `--unanswered` is how long the voice server waits for a call to be answered, 8 s by
  default instead of the real 90 s (it sets `RINGBACK_UNANSWERED` for the voice server).
- `--reask` is the longest a held call waits before the switchboard is asked again, 4 s by
  default instead of the real 10 min (`RINGBACK_REASK`).
- `--keep DIR` keeps the voice's logs, one for each start, the state file and the calls'
  transcripts.

It prints the voice's logs, what the voice said first on each call back, and the checks.
After the run, aiortc may print a traceback from the phone's side of the call that was on
when the server was killed ("Fatal write error on datagram transport"); it's the check's own.

Result on 2026-10-30, Claude Code 2.1.314, two runs: all 19 checks passed each time, in 3 min
7 s and 2 min 18 s with the models loading three times. After each restart the voice server
logged "restored 1 held calls, 3 to call back, 3 asks, 3 messages for sessions". The killed
server's Haiku processes ended 0.4 s after it. The held call asked the switchboard again 4.95
and 4.91 s after the voice server listened (`--reask` 4 s). Called back after the SIGKILL, the
voice opened the missed call with "Hi, you missed a call from the session a minute ago. The
config parser crash is fixed…" (once "Hi, I'm calling about the config parser crash…", which
the call back's lead asks it not to say; nothing here checks that), the call placed to ask
with "Hi, the session needs your permission to delete the build folder… Is that okay, yes or
no?", and both permissions were allowed by the spoken yes. The server killed with SIGTERM
during the answered call left that call's Haiku session folder in `~/.claude/projects`; the
one killed with SIGKILL, with no call on, left none.
