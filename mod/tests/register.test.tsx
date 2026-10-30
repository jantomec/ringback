import type { Register } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'

type On = Parameters<Parameters<typeof test>[1]>[1]

// What the voice server answers, by path: `live` says whether you're on a call with the
// session, and `waiting` is what the voice passed on for it. A call is `placed` with that
// answer (by default `{ ok: true }`) and `placedStatus` (200). A permission or question put
// to you is `created` (by default 200, `p1` or `q1`), and each long-poll for its answer takes
// the next of `answers`.
// With none left, it waits `pollMs` (25 s) on `clock` and answers pending, `held` while that's
// set; without a clock it answers 404, so a test that scripts every answer can't hang.
type Voice = {
  live?: boolean
  waiting?: object[]
  placed?: object
  placedStatus?: number
  created?: { status: number; body: object }
  answers?: object[]
  clock?: { sleep: (ms: number) => Promise<void> }
  pollMs?: number
  held?: boolean
  // The session's folder can't be read: the mod's hook fails.
  isCwdBroken?: boolean
  // The voice server isn't running: its socket refuses every request while this is set.
  isDown?: boolean
  // What the mod's store holds at the start.
  stored?: Record<string, unknown>
}

// What the engine beneath the mod is asked, and what the tests answer it. `logs` are the dim
// lines shown in the transcript, `debug` those in the debug log, `toasts` the toasts, `stored`
// the mod's store, and `slept` each wait the mod asked of a host `sleep`, which takes that
// long on `voice.clock`.
function engine(on: On, voice: Voice = {}) {
  const asked = {
    prompts: [] as string[],
    fetches: [] as { url: string; init: any }[],
    logs: [] as string[],
    debug: [] as string[],
    toasts: [] as string[],
    stored: new Map<string, unknown>(Object.entries(voice.stored ?? {})),
    slept: [] as string[],
  }
  on('store.get', (_, e) => ({ value: asked.stored.get(e.key) }))
  on('store.set', (_, e) => (asked.stored.set(e.key, e.value), { value: undefined }))
  on('store.delete', (_, e) => (asked.stored.delete(e.key), { value: undefined }))
  on('store.keys', () => ({ value: [...asked.stored.keys()] }))
  on('ui.toast', (_, e) => (asked.toasts.push(e.text), { value: undefined }))
  on('process.run', async (_, e) => {
    asked.slept.push(e.argv.join(' '))
    await voice.clock?.sleep(Number(e.argv[1]) * 1000)
    return { value: { exitCode: 0, stdout: '', stderr: '' } } as any
  })
  on('command.register', (_, e) => ({ value: { command: e.name } }))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  on('session.id', () => ({ value: 'session-1234' }))
  on('session.cwd', () => {
    if (voice.isCwdBroken) throw new Error('no folder')
    return { value: '/home/someone/Projects/demo' }
  })
  on('session.messages', () => ({
    value: [
      { role: 'user', text: 'Add a second stanza to the poem.', toolUses: [] },
      { role: 'assistant', text: "I'll add the second stanza, then check the metre.", toolUses: [] },
      { role: 'assistant', text: '', toolUses: [] },
    ],
  }))
  on('env.get', () => ({ value: '/home/someone' }))
  on('prompt.submit', (_, e) => (asked.prompts.push(e.text), { text: e.text }))
  on('ui.invalidate', () => ({ value: undefined }))
  on('ui.log', (_, e) => {
    if (e.to === 'transcript') asked.logs.push(e.text)
    else asked.debug.push(e.text)
    return { value: undefined }
  })
  on('turn.start', (_, e) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: '' }))
  on('http.fetch', async (_, e) => {
    asked.fetches.push({ url: e.url, init: e.init })
    if (voice.isDown) throw new Error('connect ECONNREFUSED')
    const ok = (answer: object, status = 200) => ({
      value: { status, ok: status < 300, headers: {}, text: JSON.stringify(answer) },
    })
    if (e.url.endsWith('/news')) return ok({ live: voice.live ?? false })
    if (e.url.endsWith('/messages')) return ok({ messages: voice.waiting?.splice(0) ?? [] })
    if (e.url.endsWith('/calls')) return ok(voice.placed ?? { ok: true }, voice.placedStatus)
    if (/\/(permissions|questions)$/.test(e.url)) {
      const created = voice.created ?? { status: 200, body: { permission: 'p1', question: 'q1', call: 'placed' } }
      return ok(created.body, created.status)
    }
    if (e.url.includes('?wait=')) {
      const answer = voice.answers?.shift()
      if (answer) return ok(answer)
      if (!voice.clock) return ok({ error: 'no such permission or question' }, 404)
      await voice.clock.sleep(voice.pollMs ?? 25_000)
      const held = voice.held ? { held: true } : {}
      return ok(
        e.url.includes('/permissions/')
          ? { verdict: 'pending', said: null, ...held }
          : { status: 'pending', answers: null, said: null, ...held },
      )
    }
    return ok({ ok: true })
  })
  return asked
}

// Stands for core's side of a tool call: its verdict (`decision`), the settings hooks on a
// permission request (`asked`, the tools they were asked about; they decide nothing, unless
// `settled` says what they decide), and the tool running (`ran`). A request nothing decides
// would show the prompt in the terminal; here, with nobody to show it to, it's refused.
function tools(on: On, decision: 'allow' | 'ask' | 'deny' = 'ask', settled?: object) {
  const seen = { asked: [] as string[], ran: [] as string[] }
  on('tool.check', () => ({ decision, reason: 'Claude Code asks before a shell command changes files.' }))
  on('classic.PermissionRequest', (_, e) => (seen.asked.push(e.tool_name), settled ? { decision: settled } : {}) as any)
  on('tool.call', (_, e) => (seen.ran.push(e.tool), { result: { stdout: '', stderr: '', interrupted: false } }) as any)
  return seen
}

// A plugin above the mod, which runs on its own: it logs the answer the mod gave a permission
// request, which `answers` reads back from the debug lines. `{}`, no decision, shows the
// prompt in the terminal.
const observing: Register = on => {
  on('classic.PermissionRequest', async ($, e, next) => {
    const answer = await next(e)
    $.ui.log(`answer ${JSON.stringify(answer)}`, { to: 'debug' })
    return answer
  })
}
// One that stops waiting for the mod after a minute and refuses the call itself, which
// abandons the mod's request, as your interrupt does.
const impatient: Register = on => {
  on('classic.PermissionRequest', ($, e, next) =>
    Promise.race([
      next(e),
      $.clock.sleep(60_000).then(() => ({ decision: { behavior: 'deny' as const, message: 'Gave up waiting.' } })),
    ]),
  )
}
const above = (register: Register) => ({ plugins: [{ name: 'above', tier: 'prepend' as const, register }] })
const answers = (debug: string[]) => debug.filter(l => l.startsWith('answer ')).map(l => JSON.parse(l.slice(7)))

const bash ={ tool: 'Bash', command: 'sed -i "" s/autumn/fall/ poem.md', description: 'Change autumn to fall in the poem' } as const

const question = {
  question: 'Which rhyme scheme should the second stanza use?',
  header: 'Rhyme',
  options: [
    { label: 'ABAB', description: 'Alternating, like the first stanza' },
    { label: 'AABB', description: 'Couplets' },
  ],
  multiSelect: false,
}

// Lets the mod's unawaited work, such as placing a call, run to its end.
const settle = async () => {
  for (let i = 0; i < 2000; i++) await Promise.resolve()
}

const turn = (answer: string, more: object = {}) =>
  ({ answer, durationMs: 1000, isAborted: false, turnId: 't', reason: 'answer', ...more }) as any

const start = async ($: any) => {
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  return $.command.run({ command: 'ringback', args: '' })
}

test('off, a finished turn does nothing', async ($, on) => {
  const asked = engine(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(0)
  expect(asked.fetches.length).toBe(0)
})

test('on, a finished turn asks for a briefing, and the briefing places a call', async ($, on) => {
  const asked = engine(on)
  const ran = await start($)
  expect(ran.text).toBe('Ringback is on: this session calls your phone when it finishes.')

  await $.turn.complete(turn('All tests pass.'))
  await settle()
  expect(asked.fetches[0].url).toBe('http://voice/sessions/session-1234/news')
  expect(JSON.parse(asked.fetches[0].init.body)).toEqual({ text: 'All tests pass.' })
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('Subject: ')

  await $.turn.complete(turn('Subject: Tests pass, ready to merge.\n\nI fixed the parser. It needs your review.'))
  await settle()
  const calls = asked.fetches.filter(f => f.url === 'http://voice/calls')
  expect(calls.length).toBe(1)
  expect(calls[0].init.socketPath).toBe('/home/someone/.local/state/ringback/voice.sock')
  expect(JSON.parse(calls[0].init.body)).toEqual({
    session_id: 'session-1234',
    project: 'demo',
    cwd: '/home/someone/Projects/demo',
    subject: 'Tests pass, ready to merge',
    briefing: 'I fixed the parser. It needs your review.',
  })
  // The next finished turn asks for a briefing again.
  await $.turn.complete(turn('Merged.'))
  await settle()
  expect(asked.prompts.length).toBe(2)
})

test("on a call you're on, a finished turn is news for it, not a new call", async ($, on) => {
  const asked = engine(on, { live: true })
  await start($)
  await $.turn.complete(turn('Pushed the branch.'))
  await settle()
  expect(asked.fetches.map(f => f.url)).toEqual(['http://voice/sessions/session-1234/news'])
  expect(asked.prompts.length).toBe(0)
})

test('what the voice passes on becomes a prompt, with the transcript', async ($, on) => {
  const waiting = [{ text: 'The developer says: push it.', transcript: '.ringback/calls/2026-10-29-1412.md' }]
  const asked = engine(on, { waiting })
  const clock = mock.clock(on)
  await start($)
  await clock.advance(1000)
  await settle()
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('The developer says: push it.')
  expect(asked.prompts[0]).toContain(".ringback/calls/2026-10-29-1412.md")
  // Collected once: the next second brings nothing new.
  await clock.advance(1000)
  await settle()
  expect(asked.prompts.length).toBe(1)
})

const unanswered = `The call about "Tests pass" wasn't answered within 1 min 30 s: declined, missed, or its audio didn't connect.`
const answered = 'The call about "Tests pass" was answered and lasted 1 min 5 s. Its transcript: .ringback/calls/x.md'

test("a call's outcome is added to the conversation and shown to you, not submitted", async ($, on) => {
  const waiting = [
    { kind: 'message', text: 'The developer says: push it.', transcript: null },
    { kind: 'outcome', text: unanswered, transcript: null },
  ]
  const asked = engine(on, { waiting })
  const session = mock.session(on)
  const clock = mock.clock(on)
  await start($)
  await clock.advance(1000)
  await settle()
  // The voice's message is still a prompt; the outcome isn't.
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('The developer says: push it.')
  const rows = session.appended()
  expect(rows.length).toBe(1)
  expect(rows[0].door).toBe('note')
  expect(rows[0].message.type).toBe('user')
  // Signed, since the model reads it inside the next prompt's message.
  expect(rows[0].message.content).toEqual([{ type: 'text', text: expect.stringContaining(unanswered) }])
  expect((rows[0].message.content[0] as { text: string }).text).toStartWith('A note from ringback')
  expect(asked.logs).toEqual([unanswered])
})

test("a call's outcome waits while a turn runs, and is added when it ends", async ($, on) => {
  const waiting: object[] = []
  const asked = engine(on, { waiting })
  const session = mock.session(on)
  const clock = mock.clock(on)
  await start($)
  await $.turn.start({ text: 'Fix the parser.', turnId: 't' })
  waiting.push({ kind: 'outcome', text: answered, transcript: '.ringback/calls/x.md' })
  await clock.advance(1000)
  await settle()
  expect(session.appended().length).toBe(0)
  expect(asked.logs.length).toBe(0)
  // A subagent's turn is no turn of the session's.
  await $.turn.start({ text: 'Find the parser.', turnId: 'a', agentId: 'agent-1' })
  await $.turn.complete(turn('Found it.', { turnId: 'a', agentId: 'agent-1' }))
  await settle()
  expect(session.appended().length).toBe(0)

  await $.turn.complete(turn('Fixed the parser.'))
  await settle()
  expect(session.appended().map(row => row.message.content)).toEqual([
    [{ type: 'text', text: expect.stringContaining(answered) }],
  ])
  expect(asked.logs).toEqual([answered])
  // The turn's end still asks for a briefing, as with no outcome waiting.
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('Subject: ')
})

// Work hours. A time on 30 or 31 October 2026 here, and as the switchboard writes it: ISO 8601
// with this machine's offset from UTC.
const at = (day: number, hour: number, minute = 0) => new Date(2026, 9, day, hour, minute)
const written = (date: Date) => {
  const offset = -date.getTimezoneOffset()
  const two = (n: number) => String(Math.floor(n)).padStart(2, '0')
  const local = new Date(date.getTime() + offset * 60_000).toISOString().slice(0, 19)
  return `${local}${offset < 0 ? '-' : '+'}${two(Math.abs(offset) / 60)}:${two(Math.abs(offset) % 60)}`
}

test('outside your work hours, a dim line says when the call rings', async ($, on) => {
  mock.clock(on, { now: at(30, 2).getTime() })
  const voice: Voice = {}
  const asked = engine(on, voice)
  await start($)
  const briefed = async () => {
    await $.turn.complete(turn('Done.'))
    await settle()
    await $.turn.complete(turn('Subject: Parser fixed\n\nI fixed the parser.'))
    await settle()
  }
  // A call that rings says nothing.
  await briefed()
  expect(asked.logs).toEqual([])
  voice.placed = { ok: true, held_until: written(at(30, 8)) }
  await briefed()
  expect(asked.logs).toEqual(['Outside your work hours: the call rings at 08:00.'])
  // Not today: with the date.
  voice.placed = { ok: true, held_until: written(at(31, 8)) }
  await briefed()
  expect(asked.logs.at(-1)).toBe('Outside your work hours: the call rings at 08:00 on Sat 31 Oct.')
  expect(asked.fetches.filter(f => f.url === 'http://voice/calls').length).toBe(3)
})

test('an interrupted briefing places no call', async ($, on) => {
  const asked = engine(on)
  await start($)
  await $.turn.complete(turn('Done.'))
  await settle()
  await $.turn.complete(turn('Subject: x', { reason: 'aborted', isAborted: true }))
  await settle()
  expect(asked.fetches.filter(f => f.url === 'http://voice/calls').length).toBe(0)
})

test('a turn that ends saying nothing brings no news and no call', async ($, on) => {
  const asked = engine(on)
  await start($)
  await $.turn.complete(turn(''))
  await $.turn.complete(turn('  \n'))
  await settle()
  expect(asked.prompts.length).toBe(0)
  expect(asked.fetches.length).toBe(0)
})

test("a subagent's turn doesn't count", async ($, on) => {
  const asked = engine(on)
  await start($)
  await $.turn.complete(turn('Found it.', { agentId: 'agent-1' }))
  await settle()
  expect(asked.prompts.length).toBe(0)
  expect(asked.fetches.length).toBe(0)
})

test('running /ringback again switches it off', async ($, on) => {
  const asked = engine(on)
  await start($)
  const ran = await $.command.run({ command: 'ringback', args: '' })
  expect(ran.text).toBe('Ringback is off.')
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(0)
})

test('while on, the line under the prompt says so', async ($, on) => {
  engine(on)
  // Stands for the engine's own line: the hint, and the tail a hook adds.
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    const props = e.props as { hint: string; tail?: string }
    return <Text>{props.hint + (props.tail ? ` · ${props.tail}` : '')}</Text>
  })
  const hint = {
    plugin: 'ringback',
    component: 'PromptHint',
    surface: 'terminal',
    props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
  } as const
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })

  let ui = await $.ui.mount(hint)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('? for shortcuts')
  await ui.unmount()

  await $.command.run({ command: 'ringback', args: '' })
  ui = await $.ui.mount(hint)
  expect((await ui.find({ type: 'Text' }))?.text).toBe('? for shortcuts · ☎ ringback on')
  await ui.unmount()
})

// The line under the prompt, as the engine draws it with the tail the mod adds.
function promptLine($: any, on: On) {
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    const props = e.props as { hint: string; tail?: string }
    return <Text>{props.hint + (props.tail ? ` · ${props.tail}` : '')}</Text>
  })
  return async () => {
    const ui = await $.ui.mount({
      plugin: 'ringback',
      component: 'PromptHint',
      surface: 'terminal',
      props: { isDraft: false, isWorking: false, hint: '? for shortcuts' },
    })
    const text = (await ui.find({ type: 'Text' }))?.text
    await ui.unmount()
    return text
  }
}

// Remembered: through a reload of the mod, and when the session is resumed.

const DAY = 24 * 3600_000

test('switching it on remembers the session by its ID, and switching it off forgets it', async ($, on) => {
  mock.clock(on, { now: at(30, 9).getTime() })
  const asked = engine(on)
  await start($)
  expect([...asked.stored]).toEqual([['on:session-1234', at(30, 9).getTime()]])
  await $.command.run({ command: 'ringback', args: '' })
  expect([...asked.stored]).toEqual([])
})

// A reload starts the module afresh: only `$.state` says it was on.
test('after a reload of the mod, ringback is still on', async ($, on) => {
  mock.clock(on)
  on('state.get', () => ({ value: { value: true, version: 1 } }))
  const asked = engine(on)
  const line = promptLine($, on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(await line()).toBe('? for shortcuts · ☎ ringback on')
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('Subject: ')
  // Silently: nothing changed for you.
  expect(asked.logs).toEqual([])
})

test('switched off before a reload, it stays off, whatever the store says', async ($, on) => {
  mock.clock(on, { now: at(30, 9).getTime() })
  on('state.get', () => ({ value: { value: false, version: 2 } }))
  const asked = engine(on, { stored: { 'on:session-1234': at(30, 8).getTime() } })
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(0)
})

test('a session resumed within 30 days has ringback on again; older ones are forgotten', async ($, on) => {
  const now = at(30, 9).getTime()
  mock.clock(on, { now })
  const asked = engine(on, {
    stored: { 'on:session-1234': now - 29 * DAY, 'on:session-old': now - 31 * DAY, 'on:session-other': now - DAY },
  })
  const line = promptLine($, on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(await line()).toBe('? for shortcuts · ☎ ringback on')
  expect(asked.logs).toEqual(['Ringback is still on: this session calls your phone when it finishes.'])
  // Its 30 days start again; the old session is forgotten, the other kept.
  expect(Object.fromEntries(asked.stored)).toEqual({ 'on:session-1234': now, 'on:session-other': now - DAY })
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(1)
})

test('a session resumed after 30 days has ringback off', async ($, on) => {
  const now = at(30, 9).getTime()
  mock.clock(on, { now })
  const asked = engine(on, { stored: { 'on:session-1234': now - 31 * DAY } })
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(asked.stored.size).toBe(0)
  await $.turn.complete(turn('Done.'))
  await settle()
  expect(asked.prompts.length).toBe(0)
  expect(asked.logs).toEqual([])
})

test('a resumed session collects what the voice passed on', async ($, on) => {
  const clock = mock.clock(on, { now: at(30, 9).getTime() })
  const waiting = [{ kind: 'message', text: 'The developer says: push it.', transcript: null }]
  const asked = engine(on, { waiting, stored: { 'on:session-1234': at(29, 9).getTime() } })
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await clock.advance(1000)
  await settle()
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('The developer says: push it.')
})

// While the voice server is down, as while it restarts.

const briefed = async ($: any, briefing = 'Subject: Parser fixed\n\nI fixed the parser.') => {
  await $.turn.complete(turn('Done.'))
  await settle()
  await $.turn.complete(turn(briefing))
  await settle()
}
const placed = (fetches: { url: string; init: any }[]) => fetches.filter(f => f.url === 'http://voice/calls')

test("a briefing the voice server can't take is offered again every 30 s, until it can", async ($, on) => {
  const clock = mock.clock(on)
  const voice: Voice = { clock, isDown: true }
  const asked = engine(on, voice)
  const line = promptLine($, on)
  await start($)
  await briefed($)
  expect(placed(asked.fetches).length).toBe(1)
  expect(asked.logs).toEqual([
    "Can't call yet: the voice server isn't running (ringback-voice serve). Trying again every 30 s for 30 min.",
  ])
  expect(asked.toasts).toEqual([])
  expect(await line()).toBe('? for shortcuts · ☎ waiting for the voice server…')
  await clock.advance(30_000)
  await settle()
  expect(placed(asked.fetches).length).toBe(2)
  // Said once.
  expect(asked.logs.length).toBe(1)

  // A newer briefing takes the place of the one waiting.
  await briefed($, 'Subject: Parser fixed, docs too\n\nI fixed the parser and its docs.')
  expect(placed(asked.fetches).length).toBe(3)
  voice.isDown = false
  await clock.advance(29_000)
  await settle()
  expect(placed(asked.fetches).length).toBe(3)
  await clock.advance(1000)
  await settle()
  const calls = placed(asked.fetches)
  expect(calls.length).toBe(4)
  expect(JSON.parse(calls[3].init.body).subject).toBe('Parser fixed, docs too')
  expect(await line()).toBe('? for shortcuts · ☎ ringback on')
  await clock.advance(5 * 60_000)
  await settle()
  expect(placed(asked.fetches).length).toBe(4)
  expect(asked.logs.length).toBe(1)
})

// 30 min of collecting once a second takes about 10 s.
test('a briefing is offered for 30 min, then given up', { timeoutMs: 60_000 }, async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, { clock, isDown: true })
  const line = promptLine($, on)
  await start($)
  await briefed($)
  await clock.advance(30 * 60_000)
  await settle()
  // At once, then every 30 s for 30 min.
  expect(placed(asked.fetches).length).toBe(61)
  expect(asked.logs.at(-1)).toBe("Couldn't call: the voice server isn't running (ringback-voice serve), for 30 min.")
  expect(await line()).toBe('? for shortcuts · ☎ ringback on')
  await clock.advance(60_000)
  expect(placed(asked.fetches).length).toBe(61)
})

test("one the voice server can't place is offered again, with why", async ($, on) => {
  const clock = mock.clock(on)
  const voice: Voice = { clock, placedStatus: 502, placed: { error: "can't reach the switchboard at 10.0.0.1:8787" } }
  const asked = engine(on, voice)
  await start($)
  await briefed($)
  expect(asked.logs).toEqual([
    "Can't call yet: can't reach the switchboard at 10.0.0.1:8787. Trying again every 30 s for 30 min.",
  ])
  voice.placedStatus = 200
  voice.placed = { ok: true }
  await clock.advance(30_000)
  await settle()
  expect(placed(asked.fetches).length).toBe(2)
  expect(asked.toasts).toEqual([])
})

test("one the voice server refuses isn't offered again", async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, { clock, placedStatus: 400, placed: { error: 'needs session_id, project and briefing' } })
  await start($)
  await briefed($)
  expect(asked.toasts).toEqual(["Can't call: needs session_id, project and briefing"])
  await clock.advance(60_000)
  await settle()
  expect(placed(asked.fetches).length).toBe(1)
  expect(asked.logs).toEqual([])
})

test("switched off, a briefing waiting for the voice server isn't offered again", async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, { clock, isDown: true })
  await start($)
  await briefed($)
  await $.command.run({ command: 'ringback', args: '' })
  await clock.advance(60_000)
  await settle()
  expect(placed(asked.fetches).length).toBe(1)
})

// Permissions by voice.

const created = (fetches: { url: string; init: any }[], kind: string) =>
  fetches.filter(f => f.url === `http://voice/sessions/session-1234/${kind}` && f.init.method === 'POST')
const polls = (fetches: { url: string; init: any }[]) => fetches.filter(f => f.url.includes('?wait=25'))
const withdrawn = (fetches: { url: string; init: any }[]) => fetches.filter(f => f.init.method === 'DELETE').map(f => f.url)

test('a permission prompt is put to you on a call, and your yes allows it', above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { answers: [{ verdict: 'pending', said: null }, { verdict: 'allow', said: 'Yes, go ahead.' }] })
  const core = tools(on)
  await start($)
  await $.tool.call(bash)
  await settle()
  const posts = created(asked.fetches, 'permissions')
  expect(posts.length).toBe(1)
  expect(posts[0].init.socketPath).toBe('/home/someone/.local/state/ringback/voice.sock')
  expect(JSON.parse(posts[0].init.body)).toEqual({
    tool: 'Bash',
    description: 'Change autumn to fall in the poem',
    input: JSON.stringify({ command: bash.command, description: bash.description }, null, 2),
    context: "I'll add the second stanza, then check the metre.",
    project: 'demo',
    cwd: '/home/someone/Projects/demo',
    subject: 'Permission to run a shell command',
  })
  // Polled until it's settled; nothing to withdraw.
  expect(polls(asked.fetches).map(f => f.url)).toEqual([
    'http://voice/sessions/session-1234/permissions/p1?wait=25',
    'http://voice/sessions/session-1234/permissions/p1?wait=25',
  ])
  expect(withdrawn(asked.fetches)).toEqual([])
  // The settings hooks were asked first, and decided nothing.
  expect(core.asked).toEqual(['Bash'])
  expect(answers(asked.debug)).toEqual([{ decision: { behavior: 'allow' } }])
  expect(core.ran).toEqual(['Bash'])
  // An allow carries no reason, so what you said is shown to you.
  expect(asked.logs).toEqual([
    'Asking you on the phone: Permission to run a shell command',
    'You allowed it on a ringback call: "Yes, go ahead."',
  ])
})

test('your no on the call refuses it, and the session reads what you said', above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { answers: [{ verdict: 'deny', said: 'No, keep autumn.' }] })
  const core = tools(on)
  await start($)
  const result = await $.tool.call(bash)
  expect(answers(asked.debug)).toEqual([
    { decision: { behavior: 'deny', message: 'The developer said no on a ringback call: "No, keep autumn."' } },
  ])
  expect(result.text).toContain('The developer said no on a ringback call: "No, keep autumn."')
  expect(core.ran).toEqual([])
})

// `none`: you hung up without answering it.
test("with no answer from the call, it's withdrawn and the terminal asks", above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { answers: [{ verdict: 'none', said: 'Hmm, not sure.' }] })
  const core = tools(on)
  await start($)
  await $.tool.call(bash)
  await settle()
  // `none` ends the wait: no more polls.
  expect(polls(asked.fetches).length).toBe(1)
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/permissions/p1'])
  // No decision: the prompt shows in the terminal.
  expect(answers(asked.debug)).toEqual([{}])
  expect(core.ran).toEqual([])
  expect(asked.logs.at(-1)).toBe("No answer from the phone, so it's asked here.")
})

test('a hook that fails leaves the prompt to the terminal, never an allow', above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { isCwdBroken: true, answers: [{ verdict: 'allow', said: 'Yes.' }] })
  const core = tools(on)
  await start($)
  await $.tool.call(bash)
  expect(answers(asked.debug)).toEqual([{}])
  expect(core.ran).toEqual([])
})

test("a call that's allowed without asking you rings nothing", async ($, on) => {
  mock.clock(on)
  const asked = engine(on)
  const core = tools(on, 'allow')
  await start($)
  await $.tool.call(bash)
  expect(core.asked).toEqual([])
  expect(core.ran).toEqual(['Bash'])
  expect(asked.fetches.length).toBe(0)
})

test("a settings hook's decision is left alone", above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on)
  const core = tools(on, 'ask', { behavior: 'allow' })
  await start($)
  await $.tool.call(bash)
  expect(answers(asked.debug)).toEqual([{ decision: { behavior: 'allow' } }])
  expect(core.ran).toEqual(['Bash'])
  expect(asked.fetches.length).toBe(0)
})

test('off, a permission prompt shows in the terminal at once', above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on)
  const core = tools(on)
  await start($)
  await $.command.run({ command: 'ringback', args: '' })
  await $.tool.call(bash)
  expect(core.asked).toEqual(['Bash'])
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.fetches.length).toBe(0)
})

test('a query rings nothing', async ($, on) => {
  mock.clock(on)
  const asked = engine(on)
  tools(on)
  await start($)
  expect((await $.tool.check({ tool: 'Bash', input: { command: bash.command } })).decision).toBe('ask')
  expect(asked.fetches.length).toBe(0)
})

test("a subagent's permission prompt is put to you too", async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { answers: [{ verdict: 'allow', said: 'Sure.' }] })
  const core = tools(on)
  await start($)
  await $.tool.call({ ...bash, agentId: 'agent-1' })
  expect(created(asked.fetches, 'permissions').length).toBe(1)
  expect(core.ran).toEqual(['Bash'])
})

test("when the voice server can't reach you, the terminal asks at once", above(observing), async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { created: { status: 502, body: { error: "the switchboard didn't answer" } } })
  tools(on)
  await start($)
  await $.tool.call(bash)
  await settle()
  expect(created(asked.fetches, 'permissions').length).toBe(1)
  expect(polls(asked.fetches).length).toBe(0)
  expect(withdrawn(asked.fetches)).toEqual([])
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.logs.at(-1)).toBe("Couldn't ask you on the phone: the switchboard didn't answer, so it's asked here.")
})

test('switching ringback off while it waits withdraws it, and the terminal asks', above(observing), async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, { clock })
  tools(on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.advance(60_000)
  expect(polls(asked.fetches).length).toBe(3)
  expect(answers(asked.debug)).toEqual([])

  // The long-poll in flight isn't waited for.
  await $.command.run({ command: 'ringback', args: '' })
  await ran
  await settle()
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/permissions/p1'])
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.logs.at(-1)).toBe("Ringback is off, so it's asked here.")
})

// 15 min of collecting once a second takes about 4 s.
test('after 15 min with no answer, the terminal asks', { timeoutMs: 30_000, ...above(observing) }, async ($, on) => {
  const clock = mock.clock(on)
  // Long-polls of 10 min, so the 15 min end while one is in flight.
  const asked = engine(on, { clock, pollMs: 10 * 60_000 })
  tools(on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.advance(14 * 60_000)
  expect(answers(asked.debug)).toEqual([])
  await clock.advance(60_000)
  await ran
  await settle()
  expect(polls(asked.fetches).length).toBe(2)
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/permissions/p1'])
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.logs.at(-1)).toBe("No answer from the phone in 15 min, so it's asked here.")
})

// 35 min of collecting once a second takes about 10 s.
test('while its call is held, a permission waits, and 15 min count from when it rings', { timeoutMs: 60_000, ...above(observing) }, async ($, on) => {
  const clock = mock.clock(on, { now: at(30, 7).getTime() })
  const voice: Voice = {
    clock,
    held: true,
    created: { status: 200, body: { permission: 'p1', call: 'held', held_until: written(at(30, 7, 30)) } },
  }
  const asked = engine(on, voice)
  tools(on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.settle()
  expect(asked.logs).toEqual([
    'Asking you on the phone: Permission to run a shell command',
    'Outside your work hours: the call rings at 07:30.',
  ])
  // Held for 20 min: still waiting, past the 15.
  await clock.advance(20 * 60_000)
  expect(answers(asked.debug)).toEqual([])
  expect(withdrawn(asked.fetches)).toEqual([])
  // It rings: the last poll that said held was at 20 min, so 15 min count from then.
  voice.held = false
  await clock.advance(14 * 60_000)
  expect(answers(asked.debug)).toEqual([])
  await clock.advance(60_000)
  await ran
  await settle()
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/permissions/p1'])
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.logs.at(-1)).toBe("No answer from the phone in 15 min, so it's asked here.")
})

// As when you answer the prompt in the terminal first.
test('an abandoned permission request is withdrawn, from a timer', above(impatient), async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, { clock })
  const core = tools(on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.advance(60_000)
  expect((await ran).text).toContain('Gave up waiting.')
  await clock.settle()
  // Sent from a timer: in a real session, a call on `$` made for the abandoned request never
  // left. The test kit sends either, so only a real session tells them apart.
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/permissions/p1'])
  expect(core.ran).toEqual([])
  // Nothing asks here: the request was abandoned.
  expect(asked.logs).toEqual(['Asking you on the phone: Permission to run a shell command'])
})

test('while a permission waits, the line under the prompt says so', async ($, on) => {
  const clock = mock.clock(on)
  const waiting: object[] = []
  engine(on, { clock, answers: waiting })
  tools(on)
  on('ui.render', ($, e) => {
    const { Text } = $.ui.resolve(e)
    const props = e.props as { hint: string; tail?: string }
    return <Text>{props.hint + (props.tail ? ` · ${props.tail}` : '')}</Text>
  })
  const line = async () => {
    const ui = await $.ui.mount({
      plugin: 'ringback',
      component: 'PromptHint',
      surface: 'terminal',
      props: { isDraft: false, isWorking: true, hint: 'esc to interrupt' },
    })
    const text = (await ui.find({ type: 'Text' }))?.text
    await ui.unmount()
    return text
  }
  await start($)
  const ran = $.tool.call(bash)
  await clock.settle()
  expect(await line()).toBe('esc to interrupt · ☎ asking you on the phone…')
  waiting.push({ verdict: 'allow', said: 'Yes.' })
  await clock.advance(25_000)
  await ran
  expect(await line()).toBe('esc to interrupt · ☎ ringback on')
})

test('while a permission waits, its turn runs on: outcomes wait, messages queue as prompts', async ($, on) => {
  const clock = mock.clock(on)
  const waiting: object[] = []
  const verdicts: object[] = []
  const asked = engine(on, { clock, waiting, answers: verdicts })
  const session = mock.session(on)
  tools(on)
  await start($)
  await $.turn.start({ text: 'Add a second stanza.', turnId: 't' })
  const ran = $.tool.call(bash)
  waiting.push(
    { kind: 'message', text: 'The developer says: also fix the title.', transcript: null },
    { kind: 'outcome', text: answered, transcript: '.ringback/calls/x.md' },
  )
  await clock.advance(1000)
  await settle()
  // Submitted: Claude Code queues it until the session is idle.
  expect(asked.prompts.length).toBe(1)
  expect(asked.prompts[0]).toContain('The developer says: also fix the title.')
  expect(session.appended().length).toBe(0)

  verdicts.push({ verdict: 'allow', said: 'Yes.' })
  await clock.advance(25_000)
  await ran
  await $.turn.complete(turn('Added the stanza.'))
  await settle()
  expect(session.appended().length).toBe(1)
})

// While the voice server can't be reached, as while it restarts. It keeps what was asked
// across a restart, so the same permission's poll carries on.

test("a long poll that can't reach the voice server is asked again every 5 s, and carries on", above(observing), async ($, on) => {
  const clock = mock.clock(on)
  const later: object[] = []
  const voice: Voice = { clock, answers: later }
  const asked = engine(on, voice)
  const core = tools(on)
  const line = promptLine($, on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.settle()
  voice.isDown = true
  // The first poll answers pending at 25 s; the next can't connect, nor those every 5 s after.
  await clock.advance(25_000)
  await settle()
  expect(asked.logs.at(-1)).toBe("Can't reach the voice server: asking again every 5 s.")
  expect(await line()).toBe('? for shortcuts · ☎ waiting for the voice server…')
  await clock.advance(20_000)
  expect(answers(asked.debug)).toEqual([])
  expect(asked.slept).toEqual(Array(5).fill('/bin/sleep 5'))
  // It's back, with the same permission, and you said yes.
  voice.isDown = false
  later.push({ verdict: 'allow', said: 'Yes.' })
  await clock.advance(5000)
  await ran
  await settle()
  expect(answers(asked.debug)).toEqual([{ decision: { behavior: 'allow' } }])
  expect(core.ran).toEqual(['Bash'])
  const urls = polls(asked.fetches).map(f => f.url)
  expect(urls.length).toBe(7)
  expect(new Set(urls)).toEqual(new Set(['http://voice/sessions/session-1234/permissions/p1?wait=25']))
  expect(created(asked.fetches, 'permissions').length).toBe(1)
  expect(withdrawn(asked.fetches)).toEqual([])
  expect(asked.logs.filter(l => l.startsWith("Can't reach")).length).toBe(1)
  expect(await line()).toBe('? for shortcuts · ☎ ringback on')
})

test('asked while the voice server is down, a permission is asked again every 5 s', above(observing), async ($, on) => {
  const clock = mock.clock(on)
  const later: object[] = []
  const voice: Voice = { clock, answers: later, isDown: true }
  const asked = engine(on, voice)
  tools(on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.advance(10_000)
  expect(created(asked.fetches, 'permissions').length).toBe(3)
  voice.isDown = false
  later.push({ verdict: 'allow', said: 'Yes.' })
  await clock.advance(5000)
  await ran
  expect(created(asked.fetches, 'permissions').length).toBe(4)
  expect(answers(asked.debug)).toEqual([{ decision: { behavior: 'allow' } }])
})

// 15 min of collecting once a second, and of asking every 5 s, takes about 5 s.
test("when the voice server doesn't come back in 15 min, the terminal asks", { timeoutMs: 30_000, ...above(observing) }, async ($, on) => {
  const clock = mock.clock(on)
  const voice: Voice = { clock }
  const asked = engine(on, voice)
  tools(on)
  await start($)
  const ran = $.tool.call(bash)
  await clock.settle()
  voice.isDown = true
  await clock.advance(15 * 60_000)
  await ran
  await settle()
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.logs.at(-1)).toBe("Couldn't reach the voice server in 15 min, so it's asked here.")
})

test('a long poll answered 404 still ends the wait: the permission is gone', above(observing), async ($, on) => {
  mock.clock(on)
  // No clock for the voice server: once the scripted answers are used up, it answers 404.
  const asked = engine(on, { answers: [{ verdict: 'pending', said: null }] })
  tools(on)
  await start($)
  await $.tool.call(bash)
  await settle()
  expect(polls(asked.fetches).length).toBe(2)
  expect(asked.slept).toEqual([])
  expect(answers(asked.debug)).toEqual([{}])
  expect(asked.logs.at(-1)).toBe("Lost the phone's answer: no such permission or question, so it's asked here.")
})

// Questions by voice.

const asking = { tool: 'AskUserQuestion', questions: [question] } as const

test('a question is answered on the call, as from the dialog', async ($, on) => {
  mock.clock(on)
  const reply = { status: 'answered', answers: { [question.question]: 'ABAB' }, said: 'Alternating, like the first.' }
  const asked = engine(on, { answers: [reply] })
  const core = tools(on)
  await start($)
  const result = await $.tool.call(asking)
  await settle()
  const posts = created(asked.fetches, 'questions')
  expect(posts.length).toBe(1)
  expect(JSON.parse(posts[0].init.body)).toEqual({
    questions: [question],
    context: "I'll add the second stanza, then check the metre.",
    project: 'demo',
    cwd: '/home/someone/Projects/demo',
    subject: 'Question: Rhyme',
  })
  expect(polls(asked.fetches).map(f => f.url)).toEqual(['http://voice/sessions/session-1234/questions/q1?wait=25'])
  // The dialog's own result: the questions, and each one's answer by its text.
  expect(result.result).toEqual({ questions: [question], answers: { [question.question]: 'ABAB' } })
  expect(result.context).toEqual([expect.stringContaining('"Alternating, like the first."')])
  expect((result.context as string[])[0]).toStartWith('A note from ringback')
  // The dialog never showed.
  expect(core.asked).toEqual([])
  expect(withdrawn(asked.fetches)).toEqual([])
})

// The voice server keeps it pending after a call you don't answer, for a call back or the
// session's next call. 10 min of collecting once a second takes about 3 s.
test("a question waits through a call you don't answer, for a call back", { timeoutMs: 30_000 }, async ($, on) => {
  const clock = mock.clock(on)
  const later: object[] = []
  const asked = engine(on, { clock, answers: later })
  const core = tools(on)
  await start($)
  const ran = $.tool.call(asking)
  await clock.advance(10 * 60_000)
  // One every 25 s, the 25th still waiting.
  expect(polls(asked.fetches).length).toBe(25)
  expect(core.asked).toEqual([])
  expect(withdrawn(asked.fetches)).toEqual([])

  // You call back, and answer it.
  later.push({ status: 'answered', answers: { [question.question]: 'AABB' }, said: 'Couplets.' })
  await clock.advance(25_000)
  const result = await ran
  expect(result.result).toEqual({ questions: [question], answers: { [question.question]: 'AABB' } })
  expect(core.asked).toEqual([])
  expect(withdrawn(asked.fetches)).toEqual([])
})

test('a question with no answer from the call shows the dialog', async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { answers: [{ status: 'none', answers: null, said: null }] })
  const core = tools(on)
  await start($)
  await $.tool.call(asking)
  await settle()
  expect(polls(asked.fetches).length).toBe(1)
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/questions/q1'])
  expect(core.asked).toEqual(['AskUserQuestion'])
  // Not put to you again as a permission.
  expect(created(asked.fetches, 'permissions').length).toBe(0)
})

test('outside your work hours, a question shows the dialog at once', async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, {
    clock,
    held: true,
    created: { status: 200, body: { question: 'q1', call: 'held', held_until: '2026-10-31T08:00:00+01:00' } },
  })
  const core = tools(on)
  await start($)
  await $.tool.call(asking)
  await settle()
  // No wait for the morning call: withdrawn at once, and the dialog shows.
  expect(polls(asked.fetches).length).toBe(0)
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/questions/q1'])
  expect(core.asked).toEqual(['AskUserQuestion'])
  expect(asked.logs.at(-1)).toBe("Outside your work hours, so it's asked here.")
})

test('off, a question shows the dialog untouched', async ($, on) => {
  mock.clock(on)
  const asked = engine(on)
  const core = tools(on)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  await $.tool.call(asking)
  expect(asked.fetches.length).toBe(0)
  expect(core.asked).toEqual(['AskUserQuestion'])
})

test("when the voice server can't reach you, a question shows the dialog", async ($, on) => {
  mock.clock(on)
  const asked = engine(on, { created: { status: 502, body: { error: "the switchboard didn't answer" } } })
  const core = tools(on)
  await start($)
  await $.tool.call(asking)
  await settle()
  expect(created(asked.fetches, 'questions').length).toBe(1)
  expect(withdrawn(asked.fetches)).toEqual([])
  expect(core.asked).toEqual(['AskUserQuestion'])
})

test('switching ringback off while a question waits shows the dialog', async ($, on) => {
  const clock = mock.clock(on)
  const asked = engine(on, { clock })
  const core = tools(on)
  await start($)
  const ran = $.tool.call(asking)
  await clock.advance(30_000)
  expect(polls(asked.fetches).length).toBe(2)
  expect(core.asked).toEqual([])
  await $.command.run({ command: 'ringback', args: '' })
  await ran
  await settle()
  expect(withdrawn(asked.fetches)).toEqual(['http://voice/sessions/session-1234/questions/q1'])
  expect(core.asked).toEqual(['AskUserQuestion'])
})
