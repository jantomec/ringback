// The ringback mod: `/ringback` switches calling on or off for this session. While it's on,
// each finished turn has the session write its call briefing, and the voice server on this
// Mac rings your phone with it (see "Ringback mod" in docs/design.md).
import type { EngineInterface, Register } from 'claude-code'

// What the session is asked to write when a turn ends. The voice, a faster model, talks
// about it on the call and knows nothing else about the session.
const BRIEFING = `Ringback is about to call the developer's phone about this session, so write the \
call briefing now. Use no tools, and reply with the briefing alone.

The first line is "Subject: " and 3 to 8 words on what the call is about, shown on the phone \
as it rings. After a blank line, write for the voice that will talk the developer through it, \
in at most 300 words:
- what you did and what's new since the developer last heard from you;
- what needs them: decisions, questions, anything blocked, and what you'd do next;
- what the voice needs to answer likely questions, such as why you chose something and what \
you didn't check.

It will be spoken, so write plain sentences: no lists, headings, code, tables or links, and \
round the numbers.`

let isOn = false
// The session is writing its briefing, in a turn this mod started.
let isBriefing = false
let isCalling = false

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'ringback',
      description: 'Call my phone when this session finishes; run again to stop',
      immediate: true,
    })
    return next(e)
  })

  on('command.run', { command: 'ringback' }, async $ => {
    isOn = !isOn
    isBriefing = false
    $.ui.invalidate('ui.render')
    return { text: isOn ? 'Ringback is on: this session calls your phone when it finishes.' : 'Ringback is off.' }
  })

  // While it's on, the dim line under the prompt says so, where Remote Control shows too.
  // A placeholder until ringback has an icon.
  on('ui.render', { component: 'PromptHint' }, ($, e, next) =>
    isOn ? next({ ...e, props: { ...e.props, tail: isCalling ? '☎ calling your phone…' : '☎ ringback on' } }) : next(e),
  )

  on('turn.complete', async ($, e, next) => {
    if (isOn && e.agentId === undefined) {
      if (isBriefing) {
        isBriefing = false
        if (e.reason === 'answer') void call($, e.answer)
      } else if (e.reason === 'answer') {
        isBriefing = true
        void $.prompt.submit({ text: BRIEFING })
      }
    }
    return next(e)
  })
}

// Gives the briefing to the voice server, which asks the switchboard to ring the phone.
async function call($: EngineInterface, answer: string): Promise<void> {
  const [first = '', ...rest] = answer.trim().split('\n')
  const hasSubject = /^\W*subject:/i.test(first)
  const subject = hasSubject ? first.replace(/^\W*subject:\s*/i, '').replace(/\W+$/, '').slice(0, 80) : ''
  const briefing = (hasSubject ? rest.join('\n') : answer).trim()
  const cwd = await $.session.cwd()
  const body = {
    session_id: await $.session.id(),
    project: cwd.split('/').filter(Boolean).pop() ?? 'Claude',
    subject,
    briefing,
  }
  isCalling = true
  $.ui.invalidate('ui.render')
  try {
    const response = await $.http.fetch('http://voice/calls', {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(body),
      socketPath: `${await $.env.get('HOME')}/.local/state/ringback/voice.sock`,
    })
    if (!response.ok) $.ui.toast(`Can't call: ${errorOf(response.text) ?? `the voice server answered ${response.status}`}`)
  } catch {
    $.ui.toast("Can't call: the voice server isn't running (ringback-voice serve)")
  } finally {
    isCalling = false
    $.ui.invalidate('ui.render')
  }
}

function errorOf(text: string): string | undefined {
  try {
    return JSON.parse(text).error
  } catch {
    return undefined
  }
}
