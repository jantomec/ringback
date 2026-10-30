import type { Register } from 'claude-code'
import { expect, mock, test } from 'claude-code/testing'

type On = Parameters<Parameters<typeof test>[1]>[1]

// What the voice server answers, by path: `live` says whether you're on a call with the
// session, and `waiting` is what the voice passed on for it. A permission or question put to
// you is `created` (by default 200, `p1` or `q1`), and each long-poll for its answer takes the
// next of `answers`. With none left, it waits `pollMs` (25 s) on `clock` and answers pending;
// without a clock it answers 404, so a test that scripts every answer can't hang.
type Voice = {
  live?: boolean
  waiting?: object[]
  created?: { status: number; body: object }
  answers?: object[]
  clock?: { sleep: (ms: number) => Promise<void> }
  pollMs?: number
  // The session's folder can't be read: the mod's hook fails.
  isCwdBroken?: boolean
}

// What the engine beneath the mod is asked, and what the tests answer it. `logs` are the dim
// lines shown in the transcript, `debug` those in the debug log.
function engine(on: On, voice: Voice = {}) {
  const asked = {
    prompts: [] as string[],
    fetches: [] as { url: string; init: any }[],
    logs: [] as string[],
    debug: [] as string[],
  }
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
    const ok = (answer: object, status = 200) => ({
      value: { status, ok: status < 300, headers: {}, text: JSON.stringify(answer) },
    })
    if (e.url.endsWith('/news')) return ok({ live: voice.live ?? false })
    if (e.url.endsWith('/messages')) return ok({ messages: voice.waiting?.splice(0) ?? [] })
    if (/\/(permissions|questions)$/.test(e.url)) {
      const created = voice.created ?? { status: 200, body: { permission: 'p1', question: 'q1', call: 'placed' } }
      return ok(created.body, created.status)
    }
    if (e.url.includes('?wait=')) {
      const answer = voice.answers?.shift()
      if (answer) return ok(answer)
      if (!voice.clock) return ok({ error: 'no such permission or question' }, 404)
      await voice.clock.sleep(voice.pollMs ?? 25_000)
      return ok(e.url.includes('/permissions/') ? { verdict: 'pending', said: null } : { status: 'pending', answers: null, said: null })
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
