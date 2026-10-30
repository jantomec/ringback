# ringback — design

## Goal

Your Claude Code sessions call your iPhone when they finish or need you. Each one tells
you what it did, and you talk it through hands-free, with the phone locked in your pocket
and on mobile data.

## Requirements

1. **Conversation.** Real back-and-forth in one call. You can interrupt Claude
   mid-sentence. It stops talking the moment it hears you and waits for you to finish,
   and it answers everything you say.
2. **One call per session.** The phone shows the call as the project's name, so the
   voice doesn't repeat it. It opens with what's new, then what needs your attention.
   Your replies, including permission approvals and answers to its questions, go to that
   session.
3. **Not now.** You can decline or leave unanswered any call, including one that comes
   in as call waiting during another call. The switchboard then tries again 5 min,
   20 min, 1 h, 2½ h and 6 h after the first ring, and holds every other call
   meanwhile. If the 6-hour try isn't answered either, the call is marked declined and
   all calls stay held until you call that session back from the app. Sessions' own
   calls never overlap: they wait in one queue.
4. **Understanding you.** Recognition copes with non-native accents. When a reply is
   unclear, the call asks instead of guessing, and a permission is only granted on a
   clear yes.
5. **Only while you're away.** You switch calling on in a session with `/ringback`, as
   `/rc` switches on Remote Control, and off again when you're back. While it's on, the
   session shows it under the prompt.
6. **A better model on request.** If Haiku can't keep up, you ask for a better model on
   the line, and the call carries on with Opus, with everything said so far.

## Architecture

```
 Mac running the session                    always-on Mac             iPhone
 session ── ringback ──briefing─▶ voice ───▶ switchboard ──VoIP push─▶ app rings
            mod      ◀─replies─── pipeline   (queue,      via APNs    (CallKit)
            (/ringback)           (STT, TTS,  retries)                    │
                                  Haiku) ◀──── call audio over Nebula ────┘
```

Sessions can run on several Macs. All of them share one switchboard, so there is one
queue, and each call's audio goes to the Mac whose session is calling.

Parts marked with a stage that isn't done, such as (Stage 7), are planned and not built
yet, unless they say they're built; the rest is built. The plan for them is under Stages.

1. **Ringback mod** (in each session, in `mod/`). A Claude Code mod is code that runs
   inside Claude Code: it's installed once, as a plugin, and loads in every session
   (Claude Code 2.1.287 or later). Docs:
   https://code.claude.com/docs/en/plugins/mods/overview.md. It does nothing until you
   switch it on:
   - `/ringback` switches calling on or off for its session, at once and without a
     Claude turn. While it's on, the dim line under the prompt ends with "☎ ringback on",
     where Remote Control shows too (a placeholder until ringback has an icon), and then
     "· Remote Control" while ringback sees Remote Control on in the session (Stage 3,
     step 7).
     **Remembered** (Stage 4; built, not yet tried on the phone): it stays on through a
     reload of the mod, and when you resume the session after Claude Code restarts
     (`claude --resume`), until you switch it off. Through a reload it's kept in `$.state`;
     across sessions, in the mod's store by the session's ID, for 30 days from the
     session's last start with it on. A resumed session keeps its ID: checked on
     2026-10-30 (Claude Code 2.1.314) with a mod that logged `$.session.id()`, a session
     started with `claude -p --session-id <id>`, then resumed with `claude -p --resume
     <id>` and with an interactive `claude --resume <id>`, saw the same ID at
     `session.start` each time. Not checked: `/resume` inside a running session, which
     switches sessions without a new `session.start`, and `/clear`, which gives the
     session a new ID.
   - When a turn ends while it's on, the mod asks the session to write its call briefing:
     a subject of 3–8 words, then what's new and what needs you, written to be spoken.
     The session has its whole context then and its prompt cache is warm, so this extra
     turn is quick and cheap. The mod gives the briefing to the voice server on the same
     Mac, which places the call. If you're on a call with that session already, the
     voice gets the session's answer as news instead. Outside your work hours, a dim line
     says when the call rings ("Outside your work hours: the call rings at 08:00."), with
     the date if it isn't today.
   - **While the voice server is down** (Stage 4; built, not yet tried on the phone), as
     while it restarts, the mod doesn't give up. A briefing the voice server doesn't take
     (it isn't running, or answers 5xx, as when it can't reach the switchboard) is kept and
     offered again every 30 s for up to 30 min, a newer briefing taking its place; a dim
     line says so once, and the line under the prompt ends with "☎ waiting for the voice
     server…". A permission or question that can't reach the voice server is asked again
     every 5 s within its 15 min, and so is a long poll for its answer; the voice server
     keeps what was asked across a restart, so the poll carries on under the same id. A
     404 still means it's gone. A hook can't wait with `$.clock.sleep`, which counts
     against its 10 s, so the 5 s are a `sleep` run on the Mac (`$.process.run`), a call on
     `$` whose wait doesn't count.
   - Once a second, it collects what the voice passed on for the session and submits each
     message to the session as a prompt, with the call transcript's path. A question the
     voice forwards goes the same way, and the session's answer goes back to the call.
   - **Call outcomes.** After each call, the session is told how it went: answered and how
     long, with the transcript's path, or not answered within 90 s. The note is added to
     its conversation, not submitted, so it starts no turn and can't set off another
     call, and Claude reads it on its next turn. It waits while a turn runs, you see it
     as a dim line, and it says it's from ringback: Claude Code adds it to the next
     prompt's message, and unsigned, Haiku took it for your words. So the briefing no
     longer says whether the call will reach you.
   - **Permissions.** When Claude Code is about to ask your permission while ringback is
     on, the mod puts it to you on a call too (`classic.PermissionRequest`, which fires
     only when you'd be asked, so auto mode's own approvals never ring). The prompt shows
     in the terminal at the same time, and whichever you answer first decides; answered
     in the terminal, the call's question is withdrawn. Your clear yes or no on the call
     decides it; on a no, the session reads your words, and a yes is shown to you as a
     dim line. If you hang up without answering, switch ringback off, or after 15 min, the
     request is withdrawn and the prompt stays. If you don't answer the call, the request
     waits on for a call back or the session's next call, within those 15 min. While the
     call is held outside your work hours, the 15 min count from when it rings.
   - **The session's questions.** When it asks you something with options
     (AskUserQuestion), the mod holds the question (`tool.call`), puts it to you on a call,
     and gives your answer back as the dialog's own, so the dialog doesn't show. Without
     an answer, it does, as for permissions. So after a call you didn't answer, the dialog
     shows only after 15 min, or once you switch ringback off.
     - **Outside your work hours** (Stage 3, step 7; built, not yet tried on the phone), a
       question waits for the held call as anything the session asks does: the session
       waits, the mod's 15 min count from when the call rings, and when your hours start the
       call rings and the voice asks it. Meanwhile the phone gets one notification that the
       call has a question (the switchboard's waiting calls), and the app lists it. From
       there you can send the question to the Claude app instead (the iPhone app's waiting
       calls): the voice server answers the mod's poll `terminal`, the mod lets the dialog
       show, and Remote Control shows it in the Claude app, with its own "action required"
       notification ("Push when actions required" in `/config`). The held call is then
       forgotten and never rings. Permissions are unchanged: their terminal prompt shows
       anyway, and with Remote Control in the Claude app too; sent with a question to the
       Claude app, they're left to that prompt.
     - **Remote Control.** The app offers that only while Remote Control is on in the
       session, and no mod API says whether it is. Claude Code's docs say that from 2.1.199
       `CLAUDE_CODE_BRIDGE_SESSION_ID` is set in the Bash tool's and hooks' commands while
       the session has an active Remote Control connection, but a mod doesn't get it: in a
       session started from an ordinary terminal, its `$.env` and the commands it runs saw
       none of the variables the docs describe that way, such as `CLAUDE_EFFORT`, which the
       Bash tool had (`tools/env-check`, 2026-10-30, Claude Code 2.1.314). Claude Code keeps
       a file for each running session, `~/.claude/sessions/<process>.json`, and while
       Remote Control was on, this Mac's session that had it was the only one whose file had
       a `bridgeSessionId` (2026-10-30). The mod reads the file whose `sessionId` is its
       session's, and takes a `bridgeSessionId` as Remote Control on. The file isn't
       documented: a Claude Code update can change it, and then the mod says off. Not yet
       seen: that the field goes when Remote Control ends, and that the mod's session ID is
       the file's. It looks with each ask and each long poll, and every 10 s while ringback
       is on, for the line under the prompt; the debug log says when it changed.
2. **Voice server's session side** (`voice/ringback_voice/sessions.py`). The voice server
   also listens on a Unix socket that only you can open
   (`~/.local/state/ringback/voice.sock`), for the mods of the sessions on its Mac:
   - For each briefing it makes a random call ID, the call's secret, and asks the
     switchboard to ring with it. The ID reaches the phone only through Apple's push, and
     the voice server accepts only an offer that carries the ID of a call it placed. A
     call rung by hand (`ringback-switchboard ring --voice`) is accepted only while the
     server runs with `--report`, and talks about that report.
   - **Thinking before you answer.** As soon as the switchboard takes the call, it starts
     the call's Haiku with the briefing, which writes its opening while the phone rings,
     as a person thinks of what to say while the other side picks up. The call's voice
     takes that session over when the phone's offer comes.
   - A call you haven't answered stays placed, with its Haiku, so the phone can offer
     again; after 90 s it's forgotten, and its session is told it wasn't answered. The
     voice server can't tell a decline from a missed call or audio that didn't connect;
     the switchboard's log can. It holds a sleep assertion while a call is placed or in
     progress.
   - **Held calls.** Outside your work hours the switchboard holds a call and says when it
     can ring (`held_until`). The voice server keeps the call without starting Haiku, and
     asks the switchboard again with the same call ID then, or after 10 min if that's
     sooner, so hours you widen apply within 10 min. It waits on the wall clock, looked at
     every 30 s, because asyncio's clock stops while the Mac sleeps; if the switchboard
     can't be reached, it asks again each minute. Once the call rings, it's like any other:
     Haiku starts, and the 90 s to answer count from then. Meanwhile:
     - the sleep assertion holds, held calls included, so the call can ring in the morning.
       If the Mac sleeps anyway, as a laptop may with its lid closed, the call should ring
       within 30 s of it waking, once your hours have started (not tried);
     - a new briefing from the same session replaces the held call's subject and briefing,
       instead of placing a second call. What the old briefing asked goes to the voice as
       notes, since the new one doesn't mention it;
     - what the session asks goes to the held call (`"call": "held"`, with `held_until`),
       and the mod's long polls answer `"held": true`. A question that joins it has the
       switchboard asked again at once, so the phone is told of it (Stage 3 step 7), and
       so does a change in Remote Control, so the app soon knows;
     - each time the switchboard is asked about a call, it hears whether the call has a
       question and whether Remote Control is on in its session, as its mod last said: with
       each ask, and each long poll (`remote_control`). That can be up to 25 s old, or as
       old as the mod's last ask; good enough for a rough version;
     - from the app's waiting calls, you can send a held call's question to the Claude app
       (`POST /claude-app` with the call's ID, over Nebula as an offer is; Stage 3 step 7).
       Everything the call asks is settled as `terminal`, so the mod lets the session's
       dialog show, and a permission's prompt stays; the call is forgotten: its asking the
       switchboard stops, it never rings, it isn't remembered for calling back, and its
       session is told "The call about "…" won't ring: the developer chose on the phone to
       answer its question in the Claude app instead, with Remote Control." A call that
       doesn't wait here gets 403; one whose session isn't listening, or that has no
       question, 409. It doesn't check Remote Control: the app does;
     - an offer for it is refused, as for an unknown call: the phone knows its ID only
       from the switchboard's waiting calls (Stage 5; built, not yet tried on the phone).
       An offer for it with `"now": true` calls it at once, without ringing, as a call back
       does: asking the switchboard again stops, and a fresh Haiku picks up, with the
       briefing and a lead that says you saw the call waiting and called it (`called_now`
       in `voice/ringback_voice/instructions.py`). It talks with the session as an answered
       call does, and its session is told "The developer called about "…" from the app
       while it waited and it lasted 2 min." Work hours don't apply: you called. A `now`
       offer for a call that rang meanwhile and is still placed is answered as any offer
       is; one for a call that doesn't wait here gets 403.
   - **Staying up** (Stage 4; built, not yet tried on the phone). It writes what it keeps
     to `voice-state.json` in the folder of its socket (`~/.local/state/ringback/`, so a
     check with a socket of its own has its own), on every change, atomically and readable
     only by you (it holds the calls' IDs), and reads it at start: held calls, with their
     briefing and what they ask (held again, and asking the switchboard when due); calls
     remembered for calling back; what missed calls asked; what sessions asked and wait
     for, with the same ids, so a mod's long poll carries on; and the messages and outcomes
     waiting for a session's mod. A call that was ringing or on during a restart has ended:
     its session is told it "ended when the voice server restarted" (with the transcript if
     you'd answered), what it asked waits as a missed call's does, and it can be called
     back. A launchd agent starts it at login and again if it stops (`docs/setup.md`);
     starting fails while Nebula is down, and launchd tries again every 30 s.
     - Offline (`tools/restart-check`, two runs, 19 checks each, 2–3 min): killed with
       SIGTERM, then with SIGKILL right after a message was left, and started again each
       time, it kept a held call (still held, its offer refused, and asking the switchboard
       again with the same ID 4.9 s after it listened), calls to call back, a missed call's
       permission (a long poll carried on under its id), the messages and outcomes waiting
       for sessions, and told a session whose call was on that it ended with the restart.
       Called back afterwards, both missed calls' voices opened with what they were about,
       and the spoken yes allowed both waiting permissions, one asked by the call back's
       briefing and one, from the call that ended, as a note.
     - It isn't small like the switchboard: measured on 2026-10-30, it held 3.1 GB of
       memory while idle (4.2 GB at its peak), with 1.7 GB of Python packages and about
       3 GB of speech models on disk; its own code is 2,685 lines. Each call also runs a
       Claude Code process for Haiku. The models load in 5 s. An option, not decided:
       load them only when a call is placed, while the phone rings, and drop them after a
       while without calls. That frees the memory, but adds up to 5 s to a call back.
   - It keeps what the voice passes on for each session until that session's mod
     collects it, and takes the session's answers back as news for the call. When a mod
     last collected tells whether its session is listening: within 30 s, as the mod
     collects once a second while ringback is on.
   - **What a session asks you** (`/sessions/{id}/permissions` and `/questions`): a
     permission or questions go to the call you're on with that session as a note, to its
     call that's still ringing once you answer, or else to a new call whose briefing asks
     it. The mod waits for the answer with long-polls (25 s at most each). The answer is
     allow or deny with your words, or each question's answer, or none if you hung up
     without one; the mod can withdraw it. If you don't answer the call, it stays
     pending, kept for the session: the next call with it you answer, a call back or the
     session's next call, gets it as a note, while the mod waits up to its 15 min. Each
     has a 4-letter id, kept 10 min after it's answered (`tools/ask-check`). Sent to the
     Claude app from a held call, the answer is `terminal`: the session asks in its own
     dialog or prompt.
   - **Calling back** (`"back": true` in the phone's offer). The voice server remembers
     each call it placed (session, project, folder, subject, briefing, and when it was
     placed) for a day after it's forgotten, also across a restart (Staying up). An
     offer marked `back` for such a call makes a new call for its session under the same
     ID: a fresh Haiku, answered at once. It talks with the session as an answered call
     does: messages, news, what the session asks, a transcript, and a call outcome ("The
     developer called back about "…" and it lasted 2 min."). An ID it doesn't remember
     gets 403, also when it takes calls rung by hand (`--report`).
     - The voice gets the missed call's briefing with a lead that says you called, how
       long ago the missed call was, and to open with what it was about, with no
       introduction (`called_back` in `voice/ringback_voice/instructions.py`). If the
       session wrote a newer briefing after it, for a later call or a held one, that
       briefing follows, "since then", and the voice opens with what changed.
     - If the session isn't listening, ringback off or the session ended, the call back
       still works, but the voice can't pass it anything or answer what it asked, and is
       told why.
     - Called back within its 90 s, a call still placed is replaced by the call back, so
       its session isn't told it wasn't answered.
     - Work hours don't apply: you called. A held call of the same session is left alone,
       to ring when your hours start, with what the session asked it; a held call can't be
       called back, since it never rang.
   - **Call transcripts.** It writes each call's transcript into the calling
     session's project, under `.ringback/calls/`: the briefing, your words as recognised,
     what the voice said and where you cut it off, and what went to the session. The
     folder holds its own `.gitignore` of `*`, so git ignores it without a change to the
     project's. Each message from the call ends with the transcript's path, so the
     session reads it when the message isn't enough. Inside the project, reading it
     needs no permission prompt, which while you're away would mean another call.
3. **Switchboard** (a daemon on one always-on Mac on the Nebula network, such as the
   lighthouse):
   - keeps one FIFO queue for the sessions on every Mac and places one call at a time
     (Stage 7; for now each `/call` rings at once, unless it's outside your work hours);
   - **waiting calls** (Stage 5; built, not yet tried on the phone): keeps the calls that
     wait (held outside your work hours, and from Stage 7 queued) in `waiting.json` in its
     state folder, with their project, subject, voice server, when they ring and when they
     were last refreshed, so they survive a restart, and gives them to the app, soonest
     first (`POST /waiting`, `{"calls": [...]}`). Only a phone that registered gets them,
     by its push token in the request: a call's ID lets whoever has it take the call. Each
     voice server's asking again every 10 min refreshes its calls; one not refreshed for
     25 min is dropped, so a call from a Mac that's gone doesn't wait forever. A call that
     rings, is called from the app (the phone's event "called now"), or whose question the
     app sent to the Claude app ("claude app"), leaves the list. A new briefing for a held
     call reaches the list with the voice server's next asking, so its subject can be up to
     10 min old. Each listed call also says whether it has a question (`question`) and
     whether Remote Control is on in its session (`remote_control`), as its voice server
     last said; a voice server from before says neither, which lists as false;
   - **a held call's question** (Stage 3 step 7; built, not yet tried on the phone): the
     first time a held call has a question, the switchboard sends each phone that gave a
     notification token one ordinary notification (an APNs alert push: `apns-push-type:
     alert`, the app's bundle ID as topic, priority 10): "\<project\> has a question", with
     the call's subject, the default sound, and the default (active) interruption level, so
     your Focus decides whether it shows and sounds, as for any app's. APNs keeps it until
     the call would ring. `waiting.json` keeps when it was sent (`notified`), so asking
     again and restarts don't repeat it. Held calls without a question stay silent. Tapping
     it opens the app. Without an APNs key it logs that instead;
   - starts a call with a VoIP push through APNs that names the Mac taking the call;
   - takes calls from the voice servers at `/call` over Nebula, with their project,
     subject and call ID;
   - **work hours:** outside the work hours of every phone (set in the app), `/call` pushes
     nothing. It answers when the first phone's hours start (`held_until`, ISO 8601 with
     that phone's offset from UTC) and logs it, and the voice server asks again by then. A
     phone without hours is always inside them. The app sends its hours and time zone with
     `/register`, and with each connection check, so a new time zone arrives within half an
     hour when you travel. `ringback-switchboard ring` ignores them, and `devices` shows
     them;
   - on "not now" (declined or unanswered), retries on the schedule in requirement 3,
     with the whole queue paused in between. After a final decline it holds all calls
     until the app calls that session back (Stage 7);
   - every 30 min, wakes the app with a background push so it checks that calls can still
     reach the phone (the connection check in the iPhone app);
   - is one Swift binary of about 470 KB that needs nothing but macOS 13 or later (Intel
     or Apple silicon), so an old Mac can run it. On an old Intel Mac it used about
     4 MB of memory. It handles requests one at a time, so a stalled connection holds up
     the next request for up to 5 s, which is within the app's 10 s timeout.
4. **Voice pipeline** (on the Mac running the calling session, in the voice server). That Mac must be awake
   anyway, and this keeps the always-on Mac light.
   - Pipecat, with Parakeet TDT 0.6B v2 (MLX) for speech-to-text, Kokoro-82M (MLX, through
     mlx-audio) with the `af_heart` voice for speech, and Claude Haiku 5.5. Pipecat has no
     Parakeet service, and its own Kokoro service is too slow (see rejected
     alternatives), so we write a small service for each.
   - The phone posts its WebRTC offer to `/offer` on the voice server (`voice/`), which
     answers it with Pipecat's SmallWebRTC connection.
   - **Speaking a reply.** Haiku streams its reply, and Kokoro speaks it in pieces:
     1. As soon as the first sentence is complete, it's spoken on its own, so the voice
        starts fast.
     2. One second before the current piece finishes playing, everything Haiku has
        completed since, up to the last finished sentence, becomes the next piece. If no
        sentence has finished yet, the next piece waits for the first one that does.
     3. This repeats until the reply ends.

     Larger pieces keep intonation and pauses natural. Pieces play exactly as Kokoro
     generates them: trimming the silence between pieces sounded worse in the listening
     test.

     In that test, speaking each sentence separately left about 0.75 s of silence
     between sentences, against 0.24–0.5 s in one continuous passage.

     The one-second lead holds because Kokoro through MLX generates speech about 27×
     faster than real time on a recent Apple silicon Mac (`tools/tts-speed`): 0.14 s for
     one sentence (about 4 s of speech) and 0.77 s for a 22 s reply. So it covers pieces
     of up to about 25 s of speech. Inside the voice, pieces of 1–24 s took 0.07–0.9 s.
     In five scripted calls (`tools/call-check`), no silence inside a reply was longer
     than the 0.6–0.8 s that Kokoro leaves where one piece ends and the next begins.
     Parakeet and Kokoro share one thread, so they never run at the same time.

     Kokoro's text-to-phoneme package (`misaki[en]`) pulls in torch through spaCy, though
     Kokoro doesn't use it. With it, the voice's environment takes about 1.6 GB of disk,
     against 0.5 GB before.
   - **Interruptions.** Playback stops as soon as you speak, and only for your voice.
     Everything queued is dropped. The voice then waits until you've finished and always
     answers what you said, including a short "yeah" or "mhm".

     Only your voice should reach the detector that stops it:
     1. on the phone, voice processing and the Voice Isolation mic mode remove background
        noise (you switch Voice Isolation on once, in Control Center). With AirPods or the
        phone at your ear, other people are also much farther from the microphone than
        you (how much quieter they arrive isn't measured);
     2. voice-activity detection then triggers the stop. Two settings in the app set its
        thresholds: the interruption sensitivity sets how sure and how long speech must be,
        and the background filter sets a volume threshold. That threshold is a level gate
        that ignores speech quieter than you sound in the microphone. Other people farther
        away arrive quieter, so on the Mac this gate keeps them out, not a model. Its
        default is a rough estimate that kept out a voice 20 dB below yours offline
        (Stage 2). It's absolute, so it depends on how loud a microphone delivers your
        voice. If that proves fragile, the next step is a gate set relative to your own
        level on the call.

     Removing other people's voices with a model as well (VoxLock) was built and is
     parked; see rejected alternatives.

     If a stop happens anyway and no words are recognised, the voice carries on, starting
     again from the sentence it was in.

     Haiku's session already holds the whole reply it wrote. So the next message tells it
     where it was cut off, giving the last words you actually heard.
   - Haiku runs as a Claude Code session on the Claude subscription, with no API key.
     - On a call for a session it starts when the call is placed (thinking before you
       answer); on a call rung by hand, when the phone's offer comes. It stays open until
       hang-up (`claude -p --input-format stream-json --output-format stream-json`), so it
       holds the whole call. It runs with a known `--session-id`, so Claude Code keeps it
       at `~/.claude/projects/<cwd>/<id>.jsonl`, where Opus can resume it (a better
       model); the voice deletes that folder when the call ends.
     - Pipecat has no Claude Code service, so we write one around that process.
     - Measured (`tools/reply-speed`): the first words come about 1.2 s after each of
       your turns (0.9–3.2 s), and 1.0–3.2 s on the first turn, which includes start-up.
       Neither lower effort nor turning thinking off made a clear difference.
   - The voice is a fast front end that holds the session's briefing as context, because
     a Claude Code turn is too slow for live conversation. A call rung by hand talks
     about a report file instead.
   - **Acting on the call.** Haiku passes what you want the session to do or know, and
     questions it can't answer, with `pass_to_session`, and answers what the session asks
     with `answer_permission` and `answer_question`. They're served by a small MCP server
     that `claude -p` starts for the call (`voice/ringback_voice/call_tools.py`), which
     relays to the call over a socket in its private folder.
     - Haiku says it's passing something on, or what it will answer, then calls the
       tool. The message or answer is held until you've heard the reply up to that point,
       and dropped if you cut the voice off before; Haiku is then told, and passes it
       again if you still want it. A tool call made before any words waits until the
       sentence after it has been heard: Haiku often calls first, and the old rule let an
       answer go after a single heard word. A message can't be recalled once it's passed,
       but you can send a correction.
     - When Haiku reaches for a tool after your words but before saying anything, the
       voice says "Okay." at once, which doesn't count as saying what it does: the answer
       still waits for the sentence after it. In scripted calls (`tools/call-check`) the
       pause after your yes fell from 5.3 s to 1.7 s in that case (one run each); when
       Haiku speaks first by itself, it stays 1.7–2.0 s.
     - What Haiku writes after the tool isn't spoken, if it said something before: it
       tends to say the same thing again.
     - The session's answer comes back as a note, "[The session: ...]". Pipecat holds it
       until the voice has finished speaking and you aren't, then Haiku tells you.
     - Before you've said anything, the tool passes nothing and Haiku is told so: once,
       while the phone rang, Haiku called it in its opening, which stalled the opening
       8.7 s and passed the session a message nobody had said.
     - Permissions: Haiku says in plain words what the session wants to do, and whether
       it deletes, overwrites, pushes or spends something, and takes only a clear yes or
       no; anything else, it answers and asks again. Questions: it reads them and their
       options, and takes your choice or your own words; it never chooses for you.
     - Notes from the session wait until the voice has started its opening, so Haiku
       doesn't answer a note in place of its opening.
   - **A better model.** When you ask for one ("can I get a better model on the line?"),
     Haiku says so and calls its `better_model` tool, which is held until you've heard it,
     like the others, and dropped if you cut it off. Then Haiku's process is closed and
     the session resumed with `--resume <id> --model opus`, with the same flags and tools,
     so Opus has the whole conversation, tool calls included; the session reuses its own
     record of the system prompt. Opus gets a note that it's taking over, and speaks
     first. It works on calls rung by hand too.
     - Offline (`tools/call-check`, 8 scripted calls; `tools/handover-check`, 11 runs):
       from the end of your request to Opus's first words took 6.3–6.9 s, Haiku's short
       line and then 2.5–3.2 s of silence. Opus knew the earlier conversation, and passing
       messages, the session's news and a permission all worked under it. Its first words
       came a median 0.73 s after your turns, against Haiku's 1.04 s in the same calls
       (short replies). Its first turn is mostly uncached: about as much usage as ten
       later Opus turns, once.
     - If Opus isn't available (a usage limit, or a plan without it), the voice goes
       silent, as on a Haiku error; there's no way back to Haiku.
   - Speech-to-text was chosen by measuring on accented English with `tools/stt-eval`
     (464 scripted words plus 519 of free speech):
     - Parakeet v2 got about 6% of words wrong at about 40 ms per utterance.
     - Whisper large-v3-turbo was about as accurate, but took about 0.5 s and invented
       words on short clips.
     - Letting Haiku repair the transcript from context cut Parakeet's errors on the
       scripted commands from 31 to 18, but changed nothing in the free speech.
5. **Conversation rules**, in the voice's instructions and in what the mod asks of the
   session:
   - spoken style: short sentences, rounded numbers, no tables, lists or code read
     aloud; offer detail instead of reciting it; stop at once when interrupted;
   - your words come from speech recognition, so expect similar-sounding wrong words,
     especially technical terms. Read them in context, and ask when a reply is too
     garbled to be sure;
   - act on a permission prompt only after a clear yes or no, otherwise ask again; never
     choose an option for you, and take your own words when no option fits;
   - when several things need your answer, say how many, then ask them one at a time,
     and pass the answers on together once they're all in (or when you want to stop);
     nothing the voice says after passing something on is spoken;
   - the session writes its call briefing (a subject, what's new, what needs you) in a
     form that can be spoken, and its answers to what the voice passes on go back to the
     call, where the voice puts them in spoken words.
6. **iPhone app** (Swift):
   - a PushKit VoIP push triggers CallKit, which rings on the lock screen and in your
     headphones;
   - while the phone rings, the app opens the audio connection to the voice server named
     in the push (`voice` in its payload), over Nebula. The Mac can't open it, because on
     mobile data only the phone can start a Nebula tunnel (see Stage 0).
     - **Connecting while ringing.** Once the voice server answers (reaching the Mac
       first, below), the app makes its offer with `"ringing": true`, so the connection is
       up by the time you answer. Until then it sends no microphone and plays nothing,
       and the voice server sends nothing either: silence it sent while the phone rang
       queued up on the phone and played first, so on the first such call the voice
       started about 2 s late and garbled.
       When you answer, it tells the voice server (`/answered`), which then lets the
       voice speak. If the ringing offer failed, or you answered before it went, one offer
       goes after answering, without the flag, and counts as answered. A new offer for a
       call replaces the earlier one on the Mac.
     - **Only Nebula's addresses.** The offer reaches the voice server over Nebula, so
       that path is known to work, and both sides offer only it: the phone gathers
       addresses only on its VPN adapters, which is how iOS shows Nebula to WebRTC, and
       the voice server only on the address it listens on. Offered every address, WebRTC
       also checked pairs that can't connect, such as mobile data against the Mac's
       Wi-Fi, and audio connected 2.1–2.4 s after the offer; with only Nebula's, 0.94 s
       (one call on the phone, on mobile data). If the VPN adapters give no address, the
       phone offers every adapter.
     - The audio is WebRTC, using Google's WebRTC library directly. Pipecat's iOS client
       would take over the audio session.
     - WebRTC runs in manual-audio mode: CallKit activates the voice-processing audio
       session, which provides echo cancellation, and hands it to WebRTC.
     - The offer carries all ICE candidates at once, because the Mac's server doesn't
       take them one by one.
     - **Data and weak signal.** The voice is Opus in both directions, in 20 ms packets,
       50 a second each way. On weak signal, lost and late packets hurt more than
       bandwidth. Measured offline (Stage 6), the phone played by aiortc set up as Google's
       WebRTC sets itself up from the Mac's answer (`tools/phone-check --signal`), and
       Opus on its own (`tools/opus-check`):
       - **Headers.** Each packet carries 36 bytes of RTP and SRTP, and 88 more of UDP, IP
         and Nebula over IPv4 (counted, not measured): about 50 kbps each way however
         little the voice sends, now most of the traffic.
       - **Before Stage 6** the Mac sent 96 kbps stereo (aiortc's fixed setting). In test
         calls of about 3 min where the voice spoke about a third of the time (9 calls):
         89–90 kbps of Opus while it spoke, 1.5–2.4 kbps in silence, 28–33 kbps over the
         call, about 80 kbps through Nebula. The phone sent 32 kbps mono in Opus's full
         band (31 kbps while you spoke, about 71 kbps through Nebula), without forward error
         correction (FEC, a rougher copy of each packet in the next): WebRTC turns FEC on
         only when the answer has `useinbandfec=1`, and aiortc's answer had no Opus
         parameters. The phone's offer has `minptime=10;useinbandfec=1`. (From the app's
         WebRTC package run on the Mac, and libwebrtc's source; not seen on the phone.) A
         call was about 1.1 MB a minute in all.
       - **Send less** (`voice/ringback_voice/opus.py`). The Mac sends mono at 24 kbps,
         with FEC set for 10% loss. libopus 1.6 then encodes wideband (up to 8 kHz); at 5%
         it left FEC out. In the same test calls: 23 kbps of Opus while the voice speaks,
         3.3 kbps in silence, 8–10 kbps over the call, about 59 kbps through Nebula. The
         voice also comes out about 3 dB louder: aiortc's mono-to-stereo resampling had made
         it quieter. The answer asks the phone for 24 kbps, only the band speech recognition
         uses (it hears at 16 kHz), and FEC, still as `opus/48000/2`:
         `useinbandfec=1;maxaveragebitrate=24000;maxplaybackrate=16000`. Limited to that
         band, Opus adds FEC from 1% loss on; at 24 kbps in its full band only from 10%.
         WebRTC sets the loss Opus expects from the loss the Mac reports about every
         second. The phone's stand-in then sent 21–22 kbps while you spoke, about 61 kbps
         through Nebula. A call is about 0.9 MB a minute.
       - Not sent less: DTX (almost nothing sent in silence) isn't among the options PyAV
         takes for libopus here; it would need libopus directly, as the decoder below
         uses it. Longer packets (40 ms) would halve the headers, about 25 kbps less each
         way, for 20 ms more delay each way and twice the audio lost with each packet; not
         changed.
       - **Recover.** aiortc's jitter buffer held 4 of your packets back, and on a lost one
         waited for 16 more, then dropped the 4 good ones before it too. After its first
         loss it held 14 (0.28 s) for the rest of the call. Its decoder (FFmpeg) neither
         used FEC nor concealed a loss: the gap was left out. Of 12 s of speech with 5% of
         packets lost, 8.3 s came through; with 15%, 5.0 s (`tools/opus-check`). Now a
         jitter buffer passes each packet on as soon as those before it have come, and
         gives up on a missing one once 3 later ones have; the decoder (libopus as PyAV
         ships it) rebuilds a lost packet from the next one's FEC, or else conceals it.
         All 12 s come through; 25 of the 36 packets lost at 5% had FEC in the next.
       - **Speech recognition**, through the voice server, 29 sentences (215 words) a
         call, the phone's packets lost at random, 3 calls at each loss: words heard wrong
         with 0%, 5% and 15% lost were 1.4%, 21.9% and 73.3% before, and are 1.4%, 1.7%
         and 1.9% now. With none lost, all 3 wrong words in each call were numbers written
         as digits ("200", "30"). Your words also reach speech recognition sooner: a
         median 0.27–0.31 s after you stop, at every loss, against 0.57, 0.68 and 0.93 s
         before with 0%, 5% and 15% lost (one call each, with the wait for silence). The
         phone's encoder made no difference to that, so it's the Mac's receive path;
         aiortc's buffer holding 4 packets explains 80 ms of the 0.28 s with none lost,
         the rest isn't known.
       - **Measure.** Each call's "ended" report to the switchboard carries the phone's
         WebRTC stats (in `events.jsonl`): `sent_bytes`, `sent_header_bytes`,
         `sent_packets`, `sent_lost` and `sent_jitter_ms` (as the Mac reported them), the
         same for `received_`, `concealed_pct` (how much of the voice the phone made up),
         and `rtt_ms`. The voice server logs its own side at the end of each call ("audio
         for …").
       - **On the phone** (2026-10-30, mobile data, AirPods, calls rung by hand; the
         stats as above):
         - A normal call (61 s): nothing lost either way, 4–7 ms jitter, 67–74 ms round
           trip; 12.7 kbps of Opus to the phone and 16.0 kbps from it over the call; the
           phone made up 0.2% of the voice. The wideband voice sounded no different to the
           developer ("sounds great").
         - Network Link Conditioner (Settings → Developer) slows Nebula's packets twice:
           inside the tunnel and again outside it. Its 3G gave a 478 ms round trip, where
           its 100 ms each way predicts about 270; its Very Bad Network (500 ms and 10% lost
           each way) gave 2.06–2.18 s and 12 of 40 pings lost, which 1 s and 19% each way
           predicts. A profile of its own with Very Bad Network's values on the Cellular
           interface only slows them once: 1.05–1.17 s, about 22% of pings lost.
         - 3G (42 s): nothing lost, voice clear, the voice started 1.5 s after you
           answered.
         - Very Bad Network as it comes: the app never reached the Mac (each try then
           needs two 2.1 s round trips, and gave up after 3 s); see below.
         - Very Bad Network on the cellular interface (1.06 s round trip): 10.2% of your
           packets and 9.8% of the voice's lost; the phone made up 5.3% of the voice; the
           call lasted until hung up (91 s); of the 111 words heard from you, two look wrong
           ("applet", likely "updates"; "forgive", likely "forget"; not checked with the
           developer, and not told apart from accent). The first time, before the fixes below, it ended 31 s after
           it connected.
         - Left: Edge, and what the phone's encoder sends (its rate fits 24 kbps, but its
           band isn't checked).
       - **Slow networks.** Two limits, both found on Very Bad Network and fixed:
         - aioice checks every 4–6 s during a call that the other side still wants it
           (consent, RFC 7675). It sent each check once and gave up on it after 0.5 s,
           and ended the call after 6 unanswered in a row: on a network slower than a
           0.5 s round trip, every call ended about 30 s after it connected. The voice
           server now sends each check up to 3 times and waits 3.5 s, and ends the call
           after 4 unanswered in a row (`patient_consent` in
           `voice/ringback_voice/__main__.py`). Offline (`tools/ice-check`), with a 1.1 s
           and a 2.1 s round trip, connections ended after 30–36 s before and lasted 60 s
           after; a phone that's gone is noticed after 32–36 s, as before.
         - The app's tries to reach the Mac waited 3 s each, and each takes two round
           trips: above about 1.5 s, none got through. They now wait 3 s, then 6, then 12.
           On the phone with a 1.06 s round trip, the second try got through.
     - **Reaching the Mac first.** After the phone moves from Wi-Fi to mobile data, its
       Nebula tunnel to the Mac can stay stale: in the first phone test of Stage 3 the
       offer timed out after 15 s without reaching the Mac, while the switchboard on the
       lighthouse was reachable. So from the moment the phone rings, the app tries to
       reach the voice server (`/ping`, 3 s, then 6, then 12 per try) until it answers. Each try sends
       fresh packets, so Nebula notices sooner and goes through the relay. The offer goes
       when the voice server has answered, while the phone rings or up to 45 s after you
       answer; the app reports how long reaching it took, how many tries, and whether the
       connection was made while ringing;
   - During another call (phone, WhatsApp, FaceTime), a ringback call rings as call
     waiting, with iOS's own Hold & Accept, End & Accept and Decline. The app doesn't
     end it by itself. `CXCallObserver` only records that you were on another call.
     (Apps can't add buttons to that screen.)
   - It reports each call's events to the switchboard: ringing, answered, ended (with
     its duration), declined, missed and called back. Each report is stamped with when it
     happened on the phone, and retried for about a minute. After the phone changes
     network, Nebula can take a while to reach the Mac again. The "ringing" report also
     brings the tunnel up while the phone rings.
   - **Call screens.** iOS decides which screens you see, the same for every calling app:
     - On a locked phone: slide to answer, then iOS's own call screen.
     - On an unlocked phone: Accept and Decline, then the app's own call screen.

     Ours copies iOS's style (timer above the name, round glass buttons) with only Audio
     (iOS's route picker), End and Mute. End and Mute go through CallKit, so iOS and
     AirPods stay in step.
   - **If the app is closed during a call** (killed or crashed), the call is recorded as
     ended at the last heartbeat, which is saved every 2 s, and reported to the
     switchboard at the next launch.
   - **Microphone access** is asked for when you open the app, because a locked phone in a
     call can't show the prompt. While access is off, a banner links to Ringback's page in
     Settings.
   - **Connection check.** Nebula on the phone can go off without you noticing (it did
     once, with Always On set), and then calls ring but can't connect. So the
     switchboard's background push every 30 min wakes the app, which calls the switchboard
     back over Nebula, the way a call reaches the Mac. If three tries in about 25 s fail,
     the app shows a notification: "Claude can't call you: Ringback can't reach your
     switchboard over Nebula. Check that Nebula is on." It comes again after 3 h if the
     problem stays. Until the switchboard is reachable again, a banner on the main screen
     says the same, because tapping the notification removes it. Notifications are asked
     for when you open the app.
     - On the phone (one test each): unlocked and locked, the app checked in within 2 s of
       the push. With Nebula off and the phone locked, the warning came, but later than
       35 s after the push; how much later wasn't timed.
   - **Calls:** the whole call history.
     - Each call shows its caller (the project's name; "Claude from \<project\>" until
       2026-10-30) and a 3–8 word subject.
       The session writes it with its briefing, and the push carries it.
     - A green phone and the duration mean answered. A red one means declined, missed,
       busy or failed. A call back shows as an outgoing call, with the subject of the call
       it returns.
     - The history is stored on the phone for now and will move to iCloud later.
   - **Call back.** Tapping a red call (declined, missed or failed) calls that session back
     straight away, without ringing, as in the iPhone's Phone app. The voice has the same
     context the missed call had. If the session has moved on since, it says what changed
     first (calling back, in the voice server's session side).
     - Only calls whose push named their voice server can be called back: the app keeps
       it with the call from now on, so older calls stay plain rows.
     - It's an outgoing CallKit call (`CXStartCallAction`), with the same caller name as a
       call that rings, the in-call screen, Mute and End. CallKit shows it connecting, then
       connected once its audio connects.
     - Its audio goes as a call's does: the app reaches the voice server (`/ping`), then
       offers with the missed call's ID and `"back": true`, not ringing. The voice server
       answers it at once.
     - It's a new record in the history, with its own ID, and keeps the ID of the call it
       calls back. Its events go to the switchboard under its own ID: called back (with the
       missed call's ID in a note), then ended with its duration, or audio failed.
     - If the voice server no longer knows the call (forgotten after a day), the call ends
       and an alert says "Claude can't take this call back any more." A call
       back whose audio never connected is red, and tapping it calls the same call back
       again.
   - **Waiting calls** (Stage 5; built, not yet tried on the phone). The calls that wait,
     held outside your work hours (and from Stage 7 queued), show at the top of the call
     history in orange, with a clock, the caller name and subject, and when they ring
     ("Waiting · rings at 08:00", with the date if it isn't today). The app fetches them
     from the switchboard when you open it, after it registers, and with each connection
     check. Tapping one calls it now, as a call back does: an outgoing CallKit call, no
     ringing, and its offer carries the call's ID and `"now": true`, so the voice picks
     up. It's then a record in the history under the call's own ID, shown as an outgoing
     call ("Called · 1:05"), and reported to the switchboard as "called now", then
     ended or audio failed. If the voice server refuses it (403) or can't be reached, the
     call ends, an alert says "This call isn't waiting any more." or that the Mac can't be
     reached, and the list is fetched again. Not done: a call that didn't connect stays
     red, and tapping it calls it back (`back`), which the voice server refuses while it
     still holds the call.
     - **With a question** (Stage 3 step 7; built, not yet tried on the phone), a waiting
       call says "Waiting with a question · rings at 08:00", and has a trailing swipe action,
       "Claude App" (SF Symbol `arrow.up.forward.app`, no full swipe), as Mail's and
       Messages' rows have theirs. While its session last said Remote Control is on, it's
       indigo, and a tap sends the question to the Claude app: the app reaches the voice
       server (`/ping`, as a call does, for up to 20 s), then posts `/claude-app` with the
       call's ID. Taken, the call leaves the list and the app reports "claude app" to the
       switchboard; refused, an alert says why ("This call isn't waiting any more.", "The
       session isn't listening any more…", or that the Mac can't be reached), and the list
       is fetched again. While Remote Control is off, the action is grey and its tap only
       explains, in an alert. Not `.disabled`: on a swipe action it blocks the tap but
       doesn't grey the action out, which looks ready and does nothing (`tools/swipe-check`,
       iOS 26.5 and 27.1 simulators). Waiting calls without a question have no swipe action.
       While the app is open, iOS shows no banner for the switchboard's notification (the
       app has no notification delegate); the list is fetched when you open the app.
   - **Held calls** (Stage 7). While all calls are held after a final decline, the app
     says so and shows which call to call back. Calling it back releases them.
   - **Settings:**
     - **Switchboard:** the switchboard's Nebula address. The app registers its push
       token there.
     - **Work Hours:** a switch, off by default, and while it's on, From and To times below
       it (08:00 and 20:00 to start), as with Downtime in iOS Settings. Claude calls only
       between them; a call outside them waits until they start, so a session that finishes
       at 2 am calls at 8 am. From later than To wraps past midnight (22:00–06:00), and equal
       times mean any time. They're kept as times of day, read in the phone's time zone,
       and sent to the switchboard as soon as you change them, so they apply from the next
       call. Days of the week aren't covered.
     - **Interruption Sensitivity:** a slider from "Less" to "More". More stops the voice
       at shorter, less certain speech, which suits quiet places. Less needs clearer,
       longer speech, which suits noisy ones. On the Mac it sets the confidence and length
       thresholds of voice-activity detection (`voice/ringback_voice/hearing.py`). The
       middle is Pipecat's defaults, which all calls so far used. The ends need 0.35 s or
       0.1 s of speech, with confidence to match. Only the low end has been tried,
       offline (Stage 2).
     - **Ignore Background Noise:** the background filter, the level gate above. It's a
       switch, on by default, and while it's on a slider from "Gentle" to "Strong" shows
       below it, as with Reduce Loud Audio in iOS Settings. On the Mac it sets the volume
       threshold of voice-activity detection, on Pipecat's scale: 0.6 when off (-50 LUFS,
       Pipecat's default, which all phone calls so far used), and from 0.62 to 0.8
       (-30 LUFS) when on, 0.7 in the middle (the default). The strongest would ignore
       some of your own quieter speech.
     - Like iOS's own Settings, the sliders show words, not numbers. The app sends both
       settings with each call's offer, so a change applies from the next call.
7. **Nebula** carries only the call; the push reaches the phone through Apple. The
   lighthouse must be a relay (`relay.am_relay: true`) and the Mac must list it in
   `relay.relays`. On mobile data the phone reaches the Mac only through that relay:
   direct tunnels to the phone fail, and Mobile Nebula (0.12.0) has no setting to list
   relays of its own.

## Rejected alternatives

Each entry says why it lost and what would make it worth retrying.

- **Calling through the phone network (e.g. Twilio) instead of an app.** Per-minute and
  number costs are hard to justify for a personal tool. Retry if those costs become
  negligible. As a fallback where data is too weak, it's still worth pricing (Stage 8):
  - **Gained:** it needs neither the app nor Nebula, and works where there's voice
    coverage but no usable data.
  - **Lost:**
    - Narrowband phone audio makes speech recognition worse, especially for accented
      English; wideband needs VoLTE end to end.
    - Calls over a provider stream their audio to a public internet address, which ringback
      doesn't have to open today.
  - **Cheaper routes to look at:**
    - providers cheaper than Twilio, such as SIP trunks from a local operator;
    - an LTE modem with an ordinary SIM at the lighthouse, which costs only the SIM's plan,
      but needs a modem whose call audio macOS can reach (Linux supports such modems
      better).
- **The Mac opening the Nebula tunnel to the phone.** It can't reach a phone on mobile
  data, and Mobile Nebula 0.12.0 can't list relays of its own (Stage 0). Retry if Mobile
  Nebula gains `relay.relays`.
- **Speech-to-text on the phone (Apple SpeechTranscriber).**
  - It takes no vocabulary.
  - It was weaker on accented English in the one published comparison.
  - Whether it runs on a locked phone during a call is undocumented.

  Retry if Apple adds vocabulary biasing to SpeechTranscriber, or it beats Parakeet on
  your own recordings (`tools/stt-eval`).
- **Whisper large-v3-turbo.** It was about as accurate as Parakeet v2, but about 12×
  slower (about 0.5 s per utterance), and it invented words on short clips. Retry if a
  build without the per-clip overhead appears.
- **Qwen3-ASR 1.7B and Parakeet v3.** Neither did better on the test voice, and v3's extra
  languages aren't needed for English-only calls. Retry for speakers of other languages.
- **Granite Speech 5.0 2B.** Untested: mlx-audio 0.6.2 doesn't expose its keyword list,
  which was the reason to try it. Retry once it does.
- **Apple's voices for speech, and Pocket TTS.** In a listening test the standard Apple
  voices sounded poor, and Kokoro `af_heart` was preferred over Pocket TTS. Apple's
  Premium voices weren't tried. Retry with a Premium voice.
- **Pipecat's own Kokoro service (kokoro-onnx).** On the CPU, its default, it ran 8×
  faster than real time, against 27× for MLX: 1.54 s for 12 s of speech, against 0.45 s
  (`tools/tts-speed`). Pieces longer than about 7 s of speech would leave gaps, and the
  first sentence would start about 0.35 s later. CoreML made it no faster, because it
  took only about half of the model's operations. Retry if kokoro-onnx gets much faster
  on Apple silicon.
- **One sentence per Kokoro piece.** It left about 0.75 s of silence between sentences.
  It's replaced by growing pieces.
- **Trimming the silence between Kokoro pieces.** It sounded worse than untrimmed pieces.
  Retry only with crossfades or a gentler trim.
- **Pipecat's default end-of-turn wait (3 s).** When Smart Turn judged a short turn
  ("mm-hmm", "wait") unfinished, the reply came about 4.9 s after it. Replaced by 1.5 s
  (Stage 2). Retry if calls show the voice answering the first half of your sentences.
- **A 1 s end-of-turn wait.** It split none of the recorded turns, but leaves little room
  over pauses of up to 0.74 s in free speech. Retry if short turns still feel slow and
  calls show no pauses over 1 s.
- **A one-off `claude -p` call per turn.** It took about 3.2 s, 1.7 s of it start-up.
  It's replaced by a Claude Code session that stays open for the call.
- **Haiku through the API.** Calls run on the Claude subscription instead, because this
  is a personal tool. Retry if replies through Claude Code prove too slow in Stage 2.
- **Pipecat's iOS client.** It always takes over the audio session (speaker, mixing),
  which clashes with CallKit. It's replaced by Google's WebRTC in manual-audio mode.
  Retry if the client gains a CallKit or manual-audio mode.
- **Changing interruption sensitivity during a call.** The in-call screen covers the app's
  settings, so it would need a fourth button or a slider on the call screen, and a WebRTC
  data channel to the Mac. You chose to change it between calls. Retry if calls show you
  need it mid-call, for example when moving from a quiet room to the gym.
- **VoxLock: removing other people's voices on the Mac.** Parked on the branch `voxlock`,
  with the app's recording of your voice and `tools/voxlock-delay`. You recorded your
  voice once in the app, and each call sent 3 s of it with the offer. VoxLock then passed
  only your voice to voice-activity detection and Parakeet. Offline, with another voice
  talking 6 dB below yours, none of its words got through. Without VoxLock, that call
  derailed (Stage 2). It lost because, with AirPods, other people are far from the
  microphone and arrive quieter, so a level gate (the background filter) keeps them out
  without a model. Offline, it alone kept out a voice 20 dB below yours (Stage 2).
  VoxLock, by contrast, costs something on every call:
  - an interruption stops the voice about 0.1 s later (a median 80 ms of buffering, over
    187 starts of speech, plus 23 ms of computing per 80 ms chunk);
  - Parakeet got 7.0% of words wrong instead of 5.9%, on your recordings with nobody else
    talking;
  - it can mute you for a whole call. Without a recorded sample, it learned the voice of
    whoever spoke first, and offline that was the other person. A sample recorded through
    the app's audio path rather than the call's might also not match how you sound on
    calls (untested).

  Retry if calls show other people's voices stopping the voice or heard as yours when
  they're too close or loud for a level gate to tell apart from you.
- **Ending a call as "busy" during another call.** You want to choose, so it's replaced by
  call waiting and "not now" (requirement 3).
- **The switchboard in Python** (aiohttp, run with uv). On an old Intel Mac it used
  about 50 MB of memory, and uv, its cache and the virtual environment took about 280 MB
  of disk on top of Python. The Swift binary uses about 4 MB and 470 KB. Retry if the
  switchboard needs a library that only Python has.
- **The switchboard in C.** Swift already brings it down to about 4 MB of memory. C would
  also need hand-written JSON and HTTP parsing on a network port, and its own decoding of
  the APNs key; macOS gives Swift all of these. Retry if a few MB on the always-on Mac
  ever matter.
- **A switchboard on every Mac.** That gives no single queue, so calls from two Macs could
  overlap.
- **The switchboard pinging the phone to check its connection.** A locked, idle phone
  stopped answering over Nebula within 30 s (Stage 0), so pings would raise false alarms
  while the phone is fine. It would also test the direction calls don't use, and need an
  inbound rule in Mobile Nebula for the lighthouse. Replaced by the connection check, in
  which the app calls the switchboard. Retry if background pushes prove too unreliable.
- **A connection check on a timer in the app.** iOS doesn't run apps on a timer. It runs
  background refresh when it decides, which for a rarely opened app can be a few times a
  day or never.
- **The voice pipeline on the always-on Mac.** It would load that Mac. The calling Mac
  must be awake anyway.
- **Settings hooks and an MCP channel in each session** (the plan until Stage 3). A Stop
  hook and a Notification hook would have started calls, and a channel would have
  delivered your replies and relayed permission prompts. But a custom channel loads only
  when the session starts, behind `--dangerously-load-development-channels` and its
  warning screen every time, so calling couldn't be switched on mid-session; and channels
  are a research preview. The ringback mod does all of it from inside Claude Code:
  `/ringback`, the line under the prompt, prompts for the session, and holding tool calls.
  Retry if mods lose prompt submission or tool approval.
- **Calling when the session has been idle for 60 s** (Claude Code's `idle_prompt`). It
  needs no switch, but every call would come a minute late. You chose a switch you turn
  on when you leave.
- **The session's last message as the voice's context.** Too little to talk its work
  through.
- **A fork of the whole session as the voice** (`--resume <id> --fork-session`). More
  than a call needs: the first turn slows down with a long history, and every call uses
  more of the subscription. The briefing the session writes is the middle ground. Retry
  if briefings prove too thin for the voice to answer from.
- **Tags in the voice's reply for messages to the session** (`<session>…</session>`,
  taken out before the reply is spoken). Nothing ends Haiku's turn after a tag, so it
  goes on and imagines the conversation. In an offline call (`tools/call-check`), one
  instruction became three messages in one reply, and between them Haiku wrote the
  session's answers itself ("Pushed fix-parser and opened the pull request") before the
  session had done anything. A tool call ends Haiku's turn until its result comes back.
- **Passing a message as soon as Haiku writes it.** In the same call you cut the reply
  off before hearing any of it, but its messages had already gone, one of them wrong: it
  was written before the second half of your turn arrived. Messages are now held until
  you've heard them announced.
- **Holding permission requests at `tool.check`.** Claude Code's first verdict there,
  "ask", doesn't mean you'd be asked: in auto mode its classifier settles most such calls
  itself, so the mod would have rung you for routine commands. `PermissionRequest` fires
  only when you would be asked.
- **Holding a tool call in the opening until a voice exists.** Haiku's opening is written
  while the phone rings, and once it called `pass_to_session` there: holding that call
  stalled the opening 8.7 s, and then passed the session a message nobody had said.
  Before you've spoken, the tool now passes nothing.
- **Asking the briefing for what the session didn't check.** It led the session to say it
  couldn't confirm the call would reach you: 6 of 8 briefings about ringback work said
  so (`tools/briefing-check`), and scoping the question to the session's work didn't
  stop it. Without the question, 1 of 14 did. The session is now told afterwards how the
  call went (call outcomes). Retry if briefings prove too thin on what wasn't verified.
- **Handing what a missed call asked to the terminal at once.** Until Stage 3 step 5, a call
  you didn't answer settled its permissions and questions as none, so a question showed in
  the terminal as soon as the call was forgotten, and a call back couldn't answer it. They
  now wait for the next call with the session you answer, within the mod's 15 min.
  Permission prompts show in the terminal meanwhile anyway. Retry if questions waiting
  15 min hold sessions up while you're at the terminal with ringback still on.
- **Questions outside your work hours going to the terminal at once** (Stage 3 step 7 as
  first built, 2026-10-30). The mod gave a question to the terminal as soon as its call was
  held, so Remote Control could show it in the Claude app overnight, and the morning call's
  voice could only say it waited there: the dialog takes no answer from a hook. You chose
  instead to have it wait for the call, be told by a notification, and decide from the app
  whether it goes to the Claude app. Retry if held questions keep sessions waiting overnight
  that you'd rather have answered at once.
- **A call back within the missed call's 90 s as that call itself**, with the Haiku that
  wrote its opening while it rang. That opening was written for a call the session placed,
  not one you made, and its session would be told the call was answered rather than
  called back. The call back replaces it with a fresh Haiku instead, which costs about 2 s
  more before the voice starts.
- **Unsigned call-outcome notes.** Claude Code adds a note to the next prompt's message,
  so Haiku took the note for your words and said it had been told nothing (2 of 2
  sessions). Notes now say they're from ringback.
- **A separate Haiku call for each call's subject.** The session writes the subject with
  its briefing instead, so nothing extra runs.
- **Running the voice's Haiku in the project folder, to keep Claude Code's own transcript
  of the call there.** It would load the project's CLAUDE.md into the voice and add the
  call to the project's `/resume` list, and that transcript is mostly the voice's
  instructions and call notes. The voice server writes a plain transcript into the
  project instead (call transcripts).

## Open questions

- **Showing you things.** Sometimes talking isn't enough and you need to see what a
  session is doing: images sent over iMessage, or a way to inspect large geometric
  models, perhaps screen sharing. This comes after the calling stages. Claude Code's
  official iMessage channel plugin (a research preview) can send images and relay
  permission prompts, but not AskUserQuestion questions. It needs macOS, Bun, Full Disk
  Access, and Messages signed in on the Mac where the session runs.
- **The lighthouse after a reboot.** With FileVault on and no automatic login, nothing
  runs on it after a restart, not even Nebula, until someone logs in, so no call can ring.
  It runs on battery through power cuts. Making it start by itself is a separate task,
  already noted on that machine.

## Risks (unverified)

- Right after the phone leaves Wi-Fi, its first packets once took about 16 s to reach
  the Mac, and on 2026-10-29 its offer didn't reach the Mac within the app's 15 s at all.
  The Mac's Nebula log showed no new handshake from the phone during the call, and its
  only tunnel to the phone was hours old, from when the phone was on the same Wi-Fi. The
  app now tries to reach the Mac from the moment it rings (reaching the Mac first). When
  it met a stale tunnel later that day, the call worked, but the Mac answered only the
  fourth try, 12.2 s into ringing, and audio started 10.5 s after you answered: you can
  wait that long, up to the 45 s the app waits.
- On mobile data the phone reaches the Mac only through the lighthouse's relay: it tries
  the addresses the Mac gives the lighthouse first, which include those of other VPNs on
  the Mac, and they fail ("no route to host"). On 2026-10-30, after the lighthouse had
  dropped its tunnel to the idle phone and the phone had made a new one, the phone's
  tunnel to the Mac still pointed through the old one ("unable to find host with relay"):
  it gave up on it only 15 s into the next call, and a call you hang up before then never
  reaches the Mac. The lighthouse drops the locked phone's tunnel about a minute after a
  call; the phone's Nebula has punchy (keeping that tunnel alive) off. Calls then wait up
  to about 20 s for the tunnel. Not changed: both are Nebula settings on your devices
  (punchy on the phone, `lighthouse.local_allow_list` on the Mac).
- Every placed call now costs a Haiku opening on the subscription, even if you decline
  it, and a declined call keeps its Haiku and the Mac awake until it's forgotten after
  90 s.
- If the voice server crashes mid-call, that call's Claude Code session stays in
  `~/.claude/projects`; nothing cleans it up at the next start (one folder for the call on
  during a kill, `tools/restart-check`).
- Pipecat expects a WebRTC data channel and logs a warning without one. Nothing needs one
  yet: the interruption settings go with the offer.
- The voice server replaces parts of aiortc at start, by name (`voice/ringback_voice/opus.py`):
  its Opus encoder and decoder, its audio jitter buffer and its Opus parameters, and loads
  the libopus that PyAV ships. An upgrade of aiortc or PyAV can break that; if libopus
  doesn't load, the server says so at start and keeps aiortc's buffer and decoder. It
  also replaces aioice's consent check (`patient_consent`), copied from aioice 0.10.2.
- Xcode needs device support for the phone's iOS version to install development builds.
- Only the phone's processing and the background filter keep other sounds out, so:
  - gym noise or other people's voices near you could still stop the voice, or be heard
    as yours. Offline, with the filter off, someone talking even 20 dB below you was heard
    as clearly as you (Stage 2). The filter's default kept them out, but it's absolute,
    and how loud each microphone delivers your voice on a call isn't measured. Neither is
    how well the phone's voice processing and Voice Isolation remove other people's
    voices;
  - the filter or voice processing could swallow your own quiet speech: the default
    leaves about 5 dB under your quietest speech in your AirPods recordings.

  Stage 2 tests this with real noise.
- Mods are new, so their API may change. That the terminal prompt shows while the mod
  waits on `PermissionRequest`, and that whichever answers first decides, was seen in one
  interactive run on one build; neither the hooks docs nor the mods types say it.
- Where Claude Code's built-in guard mod loads (managed settings, or a Team or Enterprise
  login), an installed mod's classic-event hooks don't run, so permissions wouldn't reach
  the phone; the terminal prompt still works.
- Whether Remote Control is on reaches the app only as the mod sees it, from Claude Code's
  undocumented file for the session (Stage 3 step 7): if an update changes it, the "Claude
  App" action stays grey. The state is as last said, up to 25 s old at
  the voice server, and as old as the app's last fetch of the list.
- The held call's question notification is an APNs alert push the switchboard hasn't sent
  before; only the phone shows whether APNs takes its headers and how iOS shows it.
- Calls are protected only by Nebula and the call's secret: any device with a
  certificate from your Nebula CA can reach the switchboard and ring you, and any program
  running as you on the calling Mac can reach the voice server's session side. The
  switchboard also gives out the IDs of waiting calls (Stage 5), but only for a registered
  phone's push token, which travels only between the phone, Apple and the switchboard. The
  voice server's state file holds call IDs too, readable only by you.
- Development builds use the APNs sandbox, and provisioning profiles expire.
- The connection check relies on background pushes, which iOS treats as low priority: it
  delivers at most a few an hour, sometimes late, and none after you swipe the app away,
  until you open it again. So a warning can come late or not at all. Without notification
  permission, only the banner shows, when you open the app.
- Nebula on the phone went off by itself once (2026-10-29, between 10:26 and 10:42), with
  Always On set. It wasn't killed for memory: its extension was running in all of the
  phone's memory-pressure reports that morning, the last at 10:26 (read from a paired Mac
  with `xcrun devicectl device info files --device <UDID> --domain-type systemCrashLogs`).
  Mobile Nebula's log starts afresh with each connection, so the cause is unknown.

## Stages

Verify each stage on the real phone (locked, on mobile data, with headphones) before the
next. A simulator run or a unit test isn't evidence for ringing, push or audio.

0. **Nebula** (done). With the phone on mobile data, the phone loads a page served by the
   Mac at its Nebula IP, then `tools/nebula-check` pings the phone from the Mac.
   Mobile Nebula drops all inbound traffic by default, so the ping needs an inbound ICMP
   rule for the Mac. Results:
   - the Mac can't open a tunnel to the phone, whether it's locked or not;
   - when the phone sends to the Mac, a tunnel comes up through the lighthouse relay in
     about 0.2 s, and the Mac can then reach the phone (about 60 ms round trip);
   - that tunnel stopped answering within 30 s of locking the phone, so a locked, idle
     phone can only be reached by push. The cause is unconfirmed: iOS suspending Nebula,
     or the carrier's NAT timing out.
1. **Ring + audio** (done). A VoIP push rings the locked phone, you answer from the
   headphones, and audio flows both ways over Nebula. Results:
   - **Ringing:** a locked phone on mobile data rang as "Claude from \<project\>" and was
     answered from AirPods.
   - **Two-way audio:** a test pipeline greeted you and played your words back a second
     later. The sound in AirPods was clear.
   - **Answer to two-way audio:**
     - 2.6 s on a locked phone on mobile data, just after Wi-Fi was turned off (one call);
     - 1.2–1.8 s on Wi-Fi (five calls).
   - **After leaving Wi-Fi:** in an early test the phone's first packets took about 16 s
     to reach the Mac. In the final test, the ringing report arrived 4 s after the push.
   - **Call waiting during a real phone call:** the call-waiting screen stayed until you
     declined. (The first version ended the call by itself after about a second; that's
     replaced by "not now", requirement 3.)
   - **Closing the app during a call:** the call is recorded as ended within 2 s of the
     real end, and reported at the next launch.
   - **Swift switchboard on an old Intel Mac** (the lighthouse): a locked phone on mobile
     data rang 5 s after the push and was answered from AirPods; two-way audio came up
     2.7 s after answering, and ringing, answered, audio connected and ended all reached
     the switchboard (one call).
2. **Conversation** (works; the tests under "Not yet tested" are left open). To pick
   speech-to-text for a new voice, record `tools/stt-eval/sentences.txt` and score the
   candidates (see `tools/stt-eval/README.md`).
   Then interrupt mid-sentence, measure how quickly speech stops when you start talking
   and how long a reply takes, and test with gym-like noise. Results so far:
   - **The conversation works** on a locked phone on mobile data, with AirPods (two
     calls). The voice opened with the report in pieces. It stopped whenever interrupted,
     Haiku knew which words had been heard, and it answered a lone "mm-hmm".
   - **Answer to first words:** 2.9 s. Haiku writes its opening while audio connects
     (2.8 s), so the voice started 0.09 s after connecting. In the first call, which
     started Haiku only once audio was connected, it took 6.1 s.
   - **After your turn:** the voice answered 1.5–2.4 s after Pipecat noticed you'd
     stopped, with Haiku's first words after 1.1–1.5 s. A short turn ("mm-hmm", "wait")
     took about 4.9 s instead (on the phone and offline): Pipecat's end-of-turn model
     (Smart Turn v3) sometimes judges it unfinished and then waits its full 3 s for more.
     With that wait cut to 1.5 s (below), the second call answered "okay", "mm-hmm",
     "wait" and a short sentence in 1.4–1.8 s.
   - **Waiting for the end of your turn** (offline, `tools/call-check`, your own
     recordings): Smart Turn kept two 18–19 s stretches of free speech, with pauses of
     up to 0.67 s, as one turn whether it waited 3, 1.5 or 1 s. In five minutes of free
     speech, no pause was longer than 0.74 s, apart from one 3.2 s hesitation. So the
     voice now waits 1.5 s, which halves the wait after a turn Smart Turn misjudges.
     A pause after a complete sentence ends the turn, as it should. If you then go on,
     the reply is dropped before it's spoken, and Haiku is told you heard none of it
     (on the phone, and offline with pauses of 0.5–1 s).
   - **Very short words can go unheard.** Your recorded "Great." (about 0.3 s) never
     triggered voice-activity detection offline. On the phone, one "wait" said while the
     voice talked wasn't heard either; said again, it was. Unknown: whether that was its
     length alone, or the phone's echo cancellation softening speech over the voice.
   - **Stopping when you talk:** 0.36–0.44 s after your speech starts, measured offline
     with `tools/call-check` (two calls), so without the network and the phone's buffer.
   - **Noise:** coughing and clapping while the voice talked didn't stop it (one call).
   - **Someone else talking**, offline (`tools/call-check --background`, one call each),
     with another Kokoro voice talking under the whole call, mixed in without the phone's
     voice processing or Voice Isolation. Levels are while there's sound:
     - with your recorded turns, interrupting twice, and the other voice 6 dB below you,
       Parakeet took its sentences as yours about 25 times, and the voice answered them.
       With nobody else talking, both interruptions stopped the voice, 0.40 and 0.59 s
       after you started;
     - with two Kokoro turns, one interrupting, and the other voice 20 dB below you, it
       still stopped the voice 3 times and was taken as you 4 times, word for word. At
       9 dB below, with the sensitivity at 0.3, it did the same and once replaced your own
       words: Parakeet heard only its sentence in a turn where you said "Wait, stop for a
       second."

     These calls ran with the volume threshold at Pipecat's default, the background filter
     off. So a quieter voice is heard as clearly as yours unless something gates by level.
     With the other voice 20 dB below you:
     - at sensitivity 0, which then also raised the volume threshold to 0.7, none of it got
       through and both your turns were heard. The interruption stopped the voice 0.54 s
       after you started (0.35–0.59 s in the other calls here); that end also needs
       0.35 s of speech instead of 0.2;
     - with only the volume threshold raised to 0.7 (about -40 LUFS), now the background
       filter's default, it started no turn and no stop, and the interruption stopped the
       voice after 0.35 s. But where it overlapped a turn of yours, Parakeet heard its last
       words too ("around six. Wait, stop for a second."): the gate decides when a turn
       starts, not what's heard in it;
     - at the app's defaults, with your four recorded turns from the 6 dB call above and
       the other voice 20 dB below you, none of it got through in 70 s. All your turns
       were heard word for word, and both interruptions stopped the voice, 0.45 and
       0.63 s after you started;
     - the margins are small. On Pipecat's volume scale (0 to 1 for -110 to -10 LUFS, so
       0.1 is 10 dB), the other voice measured 0.63–0.68 in half-second windows, against
       the gate's 0.7. The first minute of your scripted AirPods recording measured
       0.75–0.89 (median 0.84). So a gate at 0.7 keeps out voices roughly 15 dB or more
       below yours, with about 5 dB to spare under your quietest speech.
   - **VoxLock** was tried offline and parked: see rejected alternatives. Its full results
     are in `docs/design.md` on the branch `voxlock`.
   - **Not yet tested:** the sensitivity and the background filter on the phone, the
     sensitivity's high end (1), the filter's strong end, gym-like noise, and someone
     talking near you on a call. Tuning the filter waits until testers report other
     people's voices getting through, or their own being missed.
   - **Left until testers report them as real problems**, because the calls so far went
     fine: very short words going unheard (above), and a stop where no words are
     recognised (the voice should carry on; the cough and clap didn't cause one).
3. **One session** (in progress). The session finishes and calls, you reply, it acts and
   calls again. Approve a permission prompt by voice. Steps, each tried offline before
   the phone:
   1. The call: `/ringback`, the briefing, the call ID and the switchboard's `/call`. The
      phone rings with the session's subject, and the voice talks about its briefing.
      Built, and tried offline (`tools/session-check`, three runs, Haiku as the
      session's model): each call was placed 6 s after the session's first prompt, with a
      5-word subject and a briefing of 132–161 spoken words, and an offer for an unknown
      call was refused.

      On the phone (2026-10-29, an interactive session in a throwaway folder, the phone
      locked on mobile data, AirPods): the first call rang with the session's subject,
      but its audio never connected (reaching the Mac first, above). The second rang as
      "Claude from ringback-trial" with the subject "Second stanza added to the autumn
      poem", audio connected 2.8 s after answering, and the voice talked the briefing
      through and asked its one decision. In between, the session stopped silently at a
      permission prompt for a shell command until you switched it to auto mode: until
      step 3, a session with ringback on can't reach you about a prompt.
   2. Replies: what you say reaches the session, questions and answers go both ways, and
      when the session is done it calls again, or tells the call you're on. Each call's
      transcript is saved in the project for the session to read. Built, and tried
      offline, not yet on the phone:
      - The voice (`tools/call-check --session`, a scripted call run four times while
        this was refined): you answer the briefing's question, the session's news
        arrives, you wrap up. Each time, Pipecat split your answer ("Yes, push it and
        open the pull request." / "Don't backport it.") into two turns. Haiku's message
        for the first half wrongly said the backport was still undecided, and was
        dropped, because you'd cut that reply off unheard. One combined message then
        went to the session as soon as its announcement had been heard, and Haiku's first
        words on the session's news came 1.3–1.5 s after it arrived. In the last run
        the voice didn't repeat itself after passing the message.
      - The session (`tools/session-check`, one run): a message left for it was answered,
        and with no call live, the session wrote a new briefing and called again 5 s
        after the message was left.
      - On the phone (the second call above): you asked the voice to read you the whole
        poem. It said its briefing had only the new stanza and that it would ask the
        session, and passed that on 6.5 s later, once you'd heard it say so. The session's
        answer came back 6 s after that, and the voice read the poem until you stopped it.
        You found the call good. Haiku's first words came 2.4–3.2 s after your turns,
        against 1.1–1.5 s in Stage 2; why isn't measured.
      - On the phone, calling again: in a later call you asked for a third stanza and a
        call back, and hung up 2 s after the request went to the session. The session
        wrote the stanza and called again 27 s after it got the request, with the subject
        "Third stanza added, poem finished"; audio connected 2.5 s after answering.
      - **A faster start, and call outcomes** (built after these phone tests; tried
        offline, not yet on the phone):
        - Silence after you answer (`tools/phone-check`, the phone's side played on this
          Mac with aiortc, 6 s of ringing): with the offer made while ringing, a median
          0.58 s (0.53–0.67 s, eight runs); with Haiku started at the offer, as before, a
          median 2.6–2.8 s. Here WebRTC connects in about 0.06 s; on the phone it took
          2.5–2.8 s, and the silence was 3.3–4.0 s. Connecting while ringing should take
          both parts out of the silence on the phone too; untested.
        - Call outcomes (`tools/session-check`, five runs): both calls' outcomes, not
          answered, reached the session within 40 s, started no turn and no call, and
          asked at the prompt, the session quoted them.
        - On the phone (2026-10-29, on mobile data, AirPods, two calls): both connected
          while ringing, only over Nebula (0.94 and 0.38 s after the offer, 2.1 and
          1.5 s before you answered), and the voice started 0.18 and 0.37 s after the
          Mac heard you'd answered. On the first you heard it about 2 s late and garbled:
          the Mac had sent silence while the phone rang. With that held until you answer,
          the second started very quickly and sounded clean. Both calls' "answered"
          outcomes were queued for the session; whether the dim line showed wasn't
          checked. Not tried: answering the instant it rings, before audio connects.
   3. Permissions by voice, and the session's questions. Built, tried offline, and on the
      phone:
      - The voice (`tools/call-check`, scripted calls): a clear yes was allow and a clear
        no deny; "what does that do exactly?" got an explanation and the question again;
        cutting the voice off while it announced a verdict dropped it, twice; a question
        was answered with an option, and with your own words. When Pipecat split an own-
        words answer into two turns, the voice asked again instead (two runs), which ends
        as none, so the terminal prompt shows.
      - The voice server (`tools/ask-check`, one run, 22 checks): placing, a live call, a
        ringing call, two asks sharing one call, none on hang-up and when unanswered,
        withdrawing.
      - A real session (`tools/session-check --asks`, two runs, 5 checks each, about
        28 s): in default mode a command that needed permission reached the voice server,
        and the voice's yes let it run; another was refused, didn't run, and the session
        quoted your "No, don't."; in auto mode a routine command asked nothing.
      - The mod (31 tests, against stand-ins). In interactive sessions driven with
        `expect`: the terminal prompt showed at once, the phone's allow dismissed it, and
        an Esc in the terminal withdrew the call's question; a question was answered with
        no dialog shown. The session's questions can't be tried in `claude -p`, which
        doesn't offer that tool.
      - On the phone (2026-10-29, a session in default mode, the phone on mobile data,
        AirPods, two calls):
        - A permission: the call was placed as the prompt appeared, and the voice asked
          "…a command that writes the current date into a file called now.txt…, then
          shows you what it wrote. Is that okay, yes or no?". "Uh yes, it's okay." allowed
          it once you'd heard the voice confirm, the command ran, and the session's
          answer came back to the call.
        - A question came to the call instead of the terminal. Speech recognition heard
          "color.txt" as "Colour Jxty"; the voice worked out what you meant, explained,
          and asked again, and "Okay, blue." went to the session as the answer. Writing
          the file then asked a permission, which reached the call you were on 1.2 s
          later. "Okay, bye" wasn't taken as an answer; "yeah, you can write" allowed it,
          and the file held "blue".
        - The voice once told you how answers reach the session ("The answer goes to it
          once you've heard this."); its instructions now say not to, and three scripted
          calls after had no such line.
        - The first call met a stale Nebula tunnel: the phone reached the Mac after 4
          tries, 12.2 s into ringing, and audio started 10.5 s after you answered.
      - Open: the split-answer case above. Answering took longer when Haiku called the
        tool before speaking, 2.1–3.6 s after your words on the phone; the voice now says
        "Okay." at once then (acting on the call).
      - Fixed: once (`tools/ask-check`, 2026-10-30), Haiku answered a question with its
        text as the id, was told nothing waited, and answered again with the right id after
        the "Okay.". That "Okay." counted as having spoken, so the rest of its reply wasn't
        spoken ("not spoken, after passing a message"), and the answer, which waits for the
        sentence after the "Okay.", went only with the next note, 40 s later. Now only words
        after the "Okay." count, and it's said once per reply (`tools/reply-check`, 5/5; 4/5
        before the fix).
   4. A better model on the line. Built, tried offline (see "A better model"), and on the
      phone (2026-10-30, one call, the phone on mobile data, AirPods): "Can I get a better
      better model mo can I get a better model on the line?" (as recognised) got "Sure,
      I'll put Opus on.", Haiku closed and Opus started on its session in 0.49 s, and Opus
      said "Hi, it's Opus now." 5.3 s after Haiku's line, with a correct recap of the
      task. It answered your next turn 1.7 s after you stopped. Not tried on the phone:
      asking Opus about something said only before it took over.
   5. Call back from the app's call history (calling back, in the voice server's session
      side and the iPhone app). Built, and tried offline, not yet on the phone:
      - The voice server (`tools/phone-check --back`, five runs, 22 checks each, about
        2.5 min each, unanswered calls forgotten after 8 s): every check passed but one
        that wasn't the call back's (below). An unknown call was refused; the voice opened
        every call back with what the missed call was about, and with what changed when the
        session had a newer briefing; a session that wasn't listening got nothing passed on,
        and the voice said it couldn't; a permission from an unanswered call was answered on
        its call back; and called back within its time, a call's session was told only that
        you called back. From the offer to the voice's first audio took a median 2.72 s
        (2.13–3.48 s, 25 call backs), as on a call rung by hand: Haiku starts only then,
        and its first words came 1.5–2.5 s after.
      - Openings, as said: "Hi, I called about the config parser fix. It crashed on empty
        lines between sections, and the fix is two lines plus a new test… Shall I push it to
        a branch and open a pull request?"; moved on, "The parser fix is done. I pushed it to
        the fix-parser branch and opened pull request 42…". Before the lead told it not to
        say it was calling, one opened "Hi, I'm calling about the version 2.4 release
        notes", as if it had placed the call; in the four runs after, none did.
      - In one run, a session folder of a voice process was left at the end, and gone
        minutes later. The check's own voice server was already stopped by then, so it was
        likely another voice server's on this Mac; the check now counts only its own
        server's sessions.
      - What a missed call asked (`tools/ask-check`, two runs): its permissions stayed
        pending after the call was forgotten, a long poll waited on through it, and the next
        call you answered asked the one left. All 43 checks passed after merging with work
        hours; the run before had the "Okay." problem under step 3.
      - The mod (34 tests with work hours): a question stays held through 10 min of
        `pending`, and an answer that comes then is the dialog's.
      - On the phone (2026-10-30, one call): you declined a call, and tapped it in the
        history 18 s later, while the voice server still had it placed. The phone reached
        the Mac 0.4 s after the tap, audio connected 1.4 s after it, and the voice started
        1.8 s after the Mac had the call, with "I called about the header file…" and that
        call's briefing. The session's note: "The developer called back about "Header
        added for hello world function" and it lasted 30 s." Not tried on the phone: a call
        back after the 90 s, after the session called again, to a session not listening,
        and the "can't take this call back" alert.
      - The voice once told you to "call back whenever you've decided" at the end of an
        answered call, which can't be called back: only unanswered calls can. Either its
        instructions say so, or calling back answered calls becomes possible; not decided.
   6. Work hours: Claude calls only between the times you set in the app, and a call
      outside them waits until they start (the app's Settings, the switchboard, held calls
      on the voice server). Built, and tried offline, not yet on the phone:
      - The switchboard (`switchboard/smoke-test.sh`, 34 new checks, 54 in all, about
        2.4 s): a call outside the hours answers `held_until` and rings nobody, inside them
        or with no hours it rings; a range past midnight; equal times mean any time; with
        two phones it rings while either is inside, and holds until the earlier start when
        both are outside; an unknown time zone, a time that isn't HH:MM and hours without a
        zone are a 400, and leave the hours as they were; a connection check brings a new
        time zone, and one it can't read is left out with a 200; `ring` pushes anyway.
      - The voice server (`tools/ask-check`, one run, 19 new checks): a held call starts no
        Haiku and keeps the Mac awake, its offer is refused, a new briefing and a question
        go to it, and it's asked again at the switchboard's time, after the longest wait,
        and after a 503, each within 0.01 s of when it was due (the waits shortened to 4 s).
        Once it rang, Haiku started at once, and on the call the voice asked you the
        permission the old briefing had asked. An unanswered one
        was forgotten 10 s after it rang, not after it was placed.
      - The mod (two new tests, 33 in all): the dim line, today and another day; a
        permission held 20 min isn't given up, and its 15 min count from when the call
        rings.
      - On the phone (2026-10-30): changing the hours in Settings re-registered the app
        each time (08:00, 09:00, then 09:40 to 20:00, with the phone's time zone). A
        session that finished at 09:37:16 got "held until 09:40" from the switchboard; the
        voice server asked again and the call rang at 09:40:00.08. You answered, and the
        voice started 0.43 s later. You expected to see the waiting call in the app,
        which is Stage 5.
      - Not tried: a call held overnight on a Mac that sleeps or a laptop with its lid
        closed, and a time zone change while travelling.
      - Left: when the hours start, every session's held call rings at once, until Stage
        7's queue lets them wait their turn. Days of the week aren't covered: the same
        hours hold every day. If a phone is inside its hours, every phone rings, including
        those outside theirs.
   7. Questions outside your work hours wait for the call, and the phone is told (decided
      2026-10-30; it replaces questions going to the terminal at once, built earlier that
      day: see rejected alternatives). The mod holds a question on a held call as it holds
      a permission, so the session waits, and when your hours start the call rings and the
      voice asks it. The first time a held call has a question, the switchboard sends the
      phone one notification, and the app lists the call as waiting with a question. Its
      swipe action, "Claude App", sends the question to the session's own dialog instead,
      which Remote Control shows in the Claude app, and the held call is forgotten; the app
      offers it only while the session last said Remote Control is on. See "The session's
      questions", the voice server's held calls, the switchboard's "a held call's question"
      and the app's waiting calls. Built, and tried offline, not yet on the phone:
      - The mod (54 tests: 4 new, and the old step 7 test rewritten; all 5 fail on the mod
        before the change): a question on a held call waits past the 15 min, and the call's
        answer when it rings is the dialog's; a `terminal` answer shows the dialog at once,
        with "You sent it to the Claude app from the phone, so it's asked here.", and leaves
        a permission to its prompt; the ask and each long poll say whether Remote Control
        is on, as Claude Code's file for the session says, and switching on and off; the line
        under the prompt shows "· Remote Control" within 10 s, and isn't looked at while
        ringback is off. Against stand-ins: what the session files really hold was seen only
        on this Mac.
      - The voice server (`tools/ask-check`, two runs, 61 checks each, 11 of them new, all
        passed, 3 min 58 s and 3 min 15 s with the models loading): a question that joins a
        held call has the switchboard asked again 0.00 s later, with `"question": true`, and
        waits for the call; answered when it rang, the voice took the permission first ("You
        have two things to decide, and I'll take them one at a time."), and hung up, both
        were `none`. To the Claude app: refused for a call that doesn't wait (403), a
        session not listening and a call without a question (409); Remote Control from a
        question and from long polls reached the switchboard at once; then the question's
        and the permission's waiting long polls got `terminal` 0.01 s after the request,
        the call was never asked about again, an offer for it and a second try got 403,
        its session was told, and the Mac could sleep again. With the state file keeping
        which sessions have Remote Control on, `tools/restart-check` still passed all 19
        checks (one run, 2 min 13 s); it doesn't check that state itself.
      - The switchboard (`switchboard/smoke-test.sh`, 17 new checks, 92 in all): a held call
        with a question tells the phone with a notification token once (logged, without an
        APNs key), and not one without a token; asked again, and after a restart, it isn't
        told again; `waiting.json` keeps `notified`; a held call without a question isn't
        told until a question joins it; the list says whether each has a question and
        Remote Control on, as last said; the "claude app" event takes a call off the list.
      - The app builds with no Swift warnings. `.disabled` on a swipe action blocks the tap
        but doesn't grey it out (`tools/swipe-check`), so the app greys it and explains. The
        list, the swipe action, its alerts and the notification are untried: they need the
        phone. The alert push's headers are as Apple documents them; APNs hasn't been seen
        to take one.
      - Remote Control: a mod sees none of the variables Claude Code gives the Bash tool and
        hooks (`tools/env-check`), so the mod reads Claude Code's file for the session
        instead (above). The phone test settles it: with `/rc` on, the line under the prompt
        should end "· Remote Control" within 10 s, and lose it when Remote Control ends.
      - Left for the phone: the notification (shown, its sound, Focus), the listing and the
        swipe action, Remote Control detection, the question reaching the Claude app and
        being answered there, the call never ringing after, and a held question answered on
        the morning call.
   8. On the phone, in one sitting (done 2026-10-30, three calls and a call back, results
      under each step): a better model on the line (step 4), calling back a declined call
      (step 5) and work hours (step 6) worked. Not tried on the phone: questions outside
      the hours (step 7, as first built, and rebuilt since; the last call's three questions
      came from its briefing, not from AskUserQuestion). Found:
      - The voice put all three of a briefing's open choices in one turn; you want them one
        at a time, each answered before the next. Its instructions now say so, for a
        briefing's choices and for the session's questions. In three scripted calls
        (`tools/call-check --session`, the phone call's briefing) the voice then said there
        were three and asked only the first, but passed each answer on as it came and
        wrote the next question after the tool, where it isn't spoken: six questions were
        lost. So it now keeps the answers and passes them on together once it has them
        all, or when you want to stop. In three more runs it asked each in turn, confirmed
        each answer, and passed all three in one message; one opening asked the first
        question and then "Shall I go on to the first question?".
      - At the start of a held call, Haiku tried to pass the session a message before you'd
        said anything ("The developer answered the call but has not yet given a choice…").
        Nothing passed in the opening goes, so nothing reached the session.
4. **Staying up** (built, tried offline, not yet on the phone). Restarts lose nothing that
   matters:
   - The voice server keeps its state in a file and reads it at start (see "Staying up"
     under the voice server's session side), and runs as a launchd agent (`docs/setup.md`;
     installing it on your Mac asks you first).
   - The mod remembers `/ringback` for each session, through a reload and
     `claude --resume`.
   - When the voice server is down or has lost what a session asked, the mod tries again
     instead of giving up: a briefing is sent again when it's back, and a question or
     permission is asked again.
   - Checks: restart the voice server with a held call, a call to call back and a
     missed call's question waiting, and each carries on; reload the mod and resume a
     session, and ringback is still on.
   - Built, and tried offline, not yet on the phone:
     - The voice server (`tools/restart-check`, two runs, 19 checks each): see "Staying
       up". The held call, the calls to call back, the waiting permissions and long polls,
       and the messages and outcomes for sessions all carried on through a SIGTERM and a
       SIGKILL; the call that was on got its note; the killed server's Haiku processes
       ended 0.4 s after it, but left their sessions' folders (Risks). With the state file
       in use,
       `tools/ask-check` passed all 45 checks (and Stage 5's 6). `tools/phone-check
       --back` passed 21 of 22: one call back opened "Hi, I'm calling about the release
       notes…", as one did once before the lead told it not to. Plain `tools/phone-check`
       passed all but its last check, which counts every voice server's Haiku folders and
       caught another voice server's live call on this Mac.
     - The mod (15 new tests, 50 in all; 11 of the new ones fail on the mod before the
       change, the other 4 guard what shouldn't change): switched on, the session is kept
       by its ID in the store, and switched off, forgotten; after a reload (`$.state` on),
       ringback is on and a finished turn asks for a briefing; a session resumed within 30
       days is on again, with a dim line, and collects, while older ones are forgotten; a
       briefing the voice server can't take is offered again every 30 s until it can, a
       newer one taking its place, and given up after 61 tries in 30 min; a 400 isn't
       offered again; a long poll that can't connect is asked again every 5 s and carries
       on with the same id; one that never reconnects goes to the terminal after 15 min; a
       404 still ends the wait.
     - `claude --resume` keeps the session's ID (see "Remembered").
     - The launchd agent's file was written and checked with `plutil -lint`; it wasn't
       loaded, so starting at login and after a crash isn't tried.
   - Left: the switchboard on the lighthouse starting by itself after a reboot (Open
     questions); the option of loading the speech models only for calls. A briefing
     waiting for the voice server is lost if the mod reloads meanwhile.
5. **Waiting calls in the app** (built, tried offline, not yet on the phone). The
   switchboard keeps the waiting calls (see "waiting calls" under the switchboard), the app
   shows them in orange at the top of the call history, and tapping one calls it now,
   without ringing (`"now": true` in the offer).
   It's the base of Stage 7's queue. A waiting call with a question is told to the phone,
   and its question can be sent to the Claude app (Stage 3 step 7). Checks: the
   switchboard's smoke test (a held call is listed, survives a restart, leaves when it rings
   or is called, and is dropped when its voice server stops asking), the voice server
   answering a `now` offer (`tools/phone-check`), and on the phone: a call held outside your
   hours shows, and tapping it talks. Built, and tried offline, not yet on the phone:
   - The switchboard (`switchboard/smoke-test.sh`, 21 new checks, 75 in all, about 8.6 s,
     5 s of it waiting for a call to go stale): an unregistered token or none gets 403; two
     held calls are listed soonest first, with project, subject, voice server and when they
     ring; both survive a restart; asked again, a held call stays; one that rings leaves,
     and so does one the phone reports "called now"; one not asked about for the stale
     time (4 s here, `RINGBACK_WAITING_STALE`) is dropped, and each is logged.
   - The voice server (`tools/ask-check`, two runs, 6 new checks, 51 in all, about 3.5 min;
     in that tool rather than `tools/phone-check`, which has no held calls): a `now` offer
     for an unknown call gets 403; a held call is answered at once, voice heard 2.5 s after
     the offer, and no longer asked about at the switchboard; its voice opened "The config
     parser fix is done. It crashed on empty lines between sections…", without saying it
     was calling, and its session was told you called it from the app; a call that rang
     meanwhile was answered as any offer is.
   - The app builds with no Swift warnings. Its list, the tap, the alerts and the events
     are untried: they need the phone.
6. **Weak signal.** Measure, then send less and recover more (see "Data and weak signal"
   in the iPhone app). Built and tried offline (`tools/phone-check --signal`,
   `tools/opus-check`): each call's "ended" report carries the phone's WebRTC stats, and
   the voice server logs its own; the Mac sends mono at 24 kbps with forward error
   correction, and its answer asks the phone for 24 kbps in wideband with forward error
   correction; a jitter buffer and decoder of our own on the Mac rebuild or conceal your
   lost packets, where aiortc's lost several more around each. Offline, with 5% and 15% of
   your packets lost, words heard wrong fell from 21.9% and 73.3% to 1.7% and 1.9% (1.4%
   with none lost, before and after), your words reached speech recognition 0.3–0.6 s
   sooner, and a call went from about 1.1 to 0.9 MB a minute. On the phone (2026-10-30;
   "On the phone" in "Data and weak signal"): the stats arrive, the voice sounds the same,
   3G worked, and Very Bad Network worked after two fixes for slow networks (the Mac's
   consent checks, the app's tries to reach the Mac); about 10% lost each way, two of 111
   words heard from you look wrong. Left: Edge.
7. **Several sessions + retries.** A second session waits for the first call to end.
   "Not now" retries on schedule while the queue waits. A final decline holds all calls
   until a call back releases them.
8. **Later.** A phone-network fallback for when data is too weak: price SIP trunks and an
   LTE modem with a SIM at the lighthouse first (Rejected alternatives). Showing you
   things, such as images over iMessage (Open questions).
