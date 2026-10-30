// The ringback mod: `/ringback` switches calling on or off for this session. While it's on,
// each finished turn has the session write its call briefing, and the voice server on this
// Mac rings your phone with it. During the call, what you say for the session reaches it as
// a prompt, and its answer goes back to the call; if you've hung up, it calls again. After
// each call, the session is told how it went, in a note that starts no turn. A permission
// prompt, or a question the session asks you, is put to you on a call first (see "Ringback
// mod" in docs/design.md). It stays on through a reload of the mod, and when you resume the
// session after Claude Code restarts. While the voice server is down, as when it restarts,
// it tries again instead of giving up.
import type { EngineInterface, HttpResponse, Register } from 'claude-code'

// What the session is asked to write when a turn ends. The voice, a faster model, talks
// about it on the call and knows nothing else about the session.
const BRIEFING = `Ringback is about to call the developer's phone about this session, so write the \
call briefing now. Use no tools, and reply with the briefing alone.

The first line is "Subject: " and 3 to 8 words on what the call is about, shown on the phone \
as it rings. After a blank line, write for the voice that will talk the developer through it, \
in at most 300 words:
- what you did and what's new since the developer last heard from you;
- what needs them: decisions, questions, anything blocked, and what you'd do next;
- what the voice needs to answer likely questions, such as why you chose something.

Leave out the call itself, such as whether it will reach the developer: you'll be told \
afterwards how it went. It will be spoken, so write plain sentences: no lists, headings, \
code, tables or links, and round the numbers.`

// What the session reads when the voice passes something on from the call.
const fromCall = (text: string, transcript?: string) =>
  `The developer said this on a ringback call about this session, and the voice on the call \
passed it on:

${text}

Your final answer goes back to the call, where the voice tells the developer; if they've hung \
up, the session calls them again.${transcript ? ` The call's transcript so far: ${transcript}` : ''}`

// What the session reads about how one of its calls went. It reaches the model inside the
// next prompt's message, so it says who wrote it: unsigned, Haiku took it for the
// developer's words and said it hadn't been told.
const outcomeNote = (text: string) =>
  `A note from ringback, the mod that calls the developer's phone about this session, on how \
a call went (the developer didn't write this): ${text}`

// What the session reads beside the answers to its questions, when they came from a call.
const answeredNote = (said: string) =>
  `A note from ringback, the mod that calls the developer's phone about this session: the \
developer answered these questions on a call. Their words, as speech recognition heard them: "${said}"`

// Why a held tool call was allowed (shown to you) or refused (read by Claude).
const onCall = (what: string, said?: string | null) =>
  said ? `${what} on a ringback call: "${said}"` : `${what} on a ringback call.`

// Tools whose dialog a hook's allow doesn't dismiss: a question put to you, which goes to the
// call through `tool.call` instead, and a plan to approve.
const PERSON_TOOLS = new Set(['AskUserQuestion', 'ExitPlanMode'])

// How long a permission or a question waits for your answer from a call before the terminal
// asks instead. If you don't answer the call, it waits on for you to call back from the phone,
// or for the session's next call.
const HOLD_MS = 15 * 60_000

// Whether ringback is on, kept through a reload of the mod (the module's own variables start
// over). Across sessions, the store keeps each session that has it on, by its ID: resumed
// (`claude --resume`), the session keeps its ID, so ringback comes back on.
const ON = { plugin: 'ringback', key: 'isOn' } as const
const remembered = (session: string) => `on:${session}`
// A session is forgotten this long after it last started with ringback on.
const REMEMBER_MS = 30 * 24 * 3600_000

// A briefing the voice server didn't take, as while it restarts, is offered again this often,
// for this long.
const RETRY_MS = 30_000
const RETRY_FOR_MS = 30 * 60_000
// While the voice server can't be reached, what the session asks you is asked again this often.
const REACH_MS = 5000

let isOn = false
// The session is writing its briefing, in a turn this mod started.
let isBriefing = false
let isCalling = false
// Collects, once a second, what the voice passed on for this session.
let collecting: { cancel(): void } | undefined
let isCollecting = false
// A turn of the main loop is running: calls' outcomes wait for it to end.
let isTurning = false
// Calls' outcomes from the voice server, to add to the conversation between turns.
const outcomes: string[] = []
// The permissions and questions put to you on a call and still waiting: each one's stop.
const holds = new Set<(why?: string) => void>()
// Those of them waiting for the voice server to come back.
const unreached = new Set<(why?: string) => void>()
// The latest briefing given to the voice server, and the one waiting to be offered again.
let latest: object | undefined
let retry: { body: object; timer: { cancel(): void } } | undefined

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    await $.command.register({
      name: 'ringback',
      description: 'Call my phone when this session finishes; run again to stop',
      immediate: true,
    })
    await restore($)
    return next(e)
  })

  on('command.run', { command: 'ringback' }, async $ => {
    await switchTo($, !isOn)
    return { text: isOn ? 'Ringback is on: this session calls your phone when it finishes.' : 'Ringback is off.' }
  })

  // While it's on, the dim line under the prompt says so, where Remote Control shows too.
  // A placeholder until ringback has an icon.
  on('ui.render', { component: 'PromptHint' }, ($, e, next) =>
    isOn ? next({ ...e, props: { ...e.props, tail: tail() } }) : next(e),
  )

  // A permission prompt, while it's on, is also put to you on a call: your yes or no there
  // decides it. Claude Code shows the prompt in the terminal at once, beside this hook, and
  // whichever answers first decides (tried in a real session); answered in the terminal, the
  // call's question is withdrawn. PermissionRequest fires only when you'd be asked, after the
  // permission mode has had its say: in auto mode, what the classifier allows never rings
  // (`tool.check` fires before it, so it would). A failure here leaves `decided`, which has no
  // decision, so it never allows by mistake.
  on('classic.PermissionRequest', async ($, e, next) => {
    // The settings hooks' answer, if they have one.
    const decided = await next(e)
    if (!isOn || decided.decision || PERSON_TOOLS.has(e.tool_name)) return decided
    const answer = await hold($, 'permissions', await permission($, e.tool_name, e.tool_input), next.signal)
    if (answer?.verdict === 'allow') {
      // An allow carries no reason: you see it here, and Claude needs none.
      $.ui.log(onCall('You allowed it', answer.said))
      return { ...decided, decision: { behavior: 'allow' } }
    }
    if (answer?.verdict === 'deny') {
      return { ...decided, decision: { behavior: 'deny', message: onCall('The developer said no', answer.said) } }
    }
    return decided
  })

  // So is a question the session asks you: your answers come back as the dialog's would, so
  // the dialog never shows. Without them, it shows in the terminal as usual: when you hang up
  // without answering, switch ringback off, or after 15 min. A call you don't answer leaves the
  // question waiting for a call back, so the dialog shows only after those 15 min. Outside your
  // work hours it shows at once, so Remote Control can answer it.
  on('tool.call', { tool: 'AskUserQuestion' }, async ($, e, next) => {
    if (!isOn) return next(e)
    const answer = await hold($, 'questions', await questions($, e.questions), next.signal)
    if (!answer?.answers) return next(e)
    return {
      result: { questions: e.questions, answers: answer.answers },
      ...(answer.said ? { context: [answeredNote(answer.said)] } : {}),
    }
  })

  on('turn.start', ($, e, next) => {
    if (e.agentId === undefined) isTurning = true
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    if (e.agentId === undefined) {
      isTurning = false
      await addOutcomes($)
    }
    if (isOn && e.agentId === undefined) {
      if (isBriefing) {
        isBriefing = false
        if (e.reason === 'answer') void call($, e.answer)
      } else if (e.reason === 'answer' && e.answer.trim()) {
        // A turn that ends saying nothing, such as after you refused a prompt in the
        // terminal, has no news and is no reason to call.
        void finished($, e.answer)
      }
    }
    return next(e)
  })
}

// The end of the dim line under the prompt, while ringback is on.
function tail(): string {
  if (unreached.size || retry) return '☎ waiting for the voice server…'
  if (holds.size) return '☎ asking you on the phone…'
  return isCalling ? '☎ calling your phone…' : '☎ ringback on'
}

// Switches ringback on or off, and remembers it: for a reload of the mod, and by the session's
// ID for when it's resumed. Off, what waits for your answer from a call goes back to the
// terminal, and a briefing waiting for the voice server is dropped.
async function switchTo($: EngineInterface, on: boolean, isRemembered = true): Promise<void> {
  isOn = on
  isBriefing = false
  collecting?.cancel()
  collecting = isOn ? $.clock.every(1000, () => collect($)) : undefined
  if (!isOn) {
    for (const stop of holds) stop('Ringback is off')
    retry?.timer.cancel()
    retry = undefined
  }
  $.ui.invalidate('ui.render')
  if (!isRemembered) return
  try {
    await $.state.set(ON, isOn)
    const key = remembered(await $.session.id())
    if (isOn) await $.store.set(key, await $.clock.now())
    else await $.store.delete(key)
  } catch (e) {
    $.ui.log(`couldn't remember whether ringback is on: ${e}`, { to: 'debug' })
  }
}

// At the session's start, and at each reload of the mod: ringback is on again if it was on
// before the reload, or, when Claude Code starts, if this session had it on when it last ran,
// within 30 days. Older sessions are forgotten.
async function restore($: EngineInterface): Promise<void> {
  try {
    const { value } = await $.state.get(ON)
    if (value !== undefined) {
      if (value !== isOn) await switchTo($, value, false)
      return
    }
    const now = await $.clock.now()
    for (const key of await $.store.keys()) {
      const at = await $.store.get(key)
      if (key.startsWith('on:') && (typeof at !== 'number' || now - at > REMEMBER_MS)) await $.store.delete(key)
    }
    if (typeof (await $.store.get(remembered(await $.session.id()))) !== 'number') return
    await switchTo($, true)
    $.ui.log('Ringback is still on: this session calls your phone when it finishes.')
  } catch (e) {
    $.ui.log(`couldn't tell whether ringback was on: ${e}`, { to: 'debug' })
  }
}

// The voice server on this Mac, over a socket only you can open.
async function voice($: EngineInterface, path: string, body?: object, method = body ? 'POST' : 'GET') {
  return $.http.fetch(`http://voice${path}`, {
    method,
    headers: { 'content-type': 'application/json' },
    body: body && JSON.stringify(body),
    socketPath: `${await $.env.get('HOME')}/.local/state/ringback/voice.sock`,
  })
}

// A finished turn: news for the call you're on, or else a briefing for a new call.
async function finished($: EngineInterface, answer: string): Promise<void> {
  try {
    const response = await voice($, `/sessions/${await $.session.id()}/news`, { text: answer })
    if (response.ok && JSON.parse(response.text).live) return
  } catch {
    // No voice server: the call below says so.
  }
  if (!isOn) return
  isBriefing = true
  void $.prompt.submit({ text: BRIEFING })
}

// Submits what the voice passed on for this session, each as a prompt of its own. A call's
// outcome isn't a prompt: it's added to the conversation.
async function collect($: EngineInterface): Promise<void> {
  if (!isOn || isCollecting) return
  isCollecting = true
  try {
    const response = await voice($, `/sessions/${await $.session.id()}/messages`)
    if (!response.ok) return
    for (const m of JSON.parse(response.text).messages as { kind?: string; text: string; transcript?: string }[]) {
      if (m.kind === 'outcome') outcomes.push(m.text)
      else void $.prompt.submit({ text: fromCall(m.text, m.transcript) })
    }
  } catch {
    // The voice server isn't running; nothing can be waiting.
  } finally {
    isCollecting = false
  }
  await addOutcomes($)
}

// Adds the calls' outcomes to the conversation, and shows each to you in a dim line. Claude
// reads them on its next turn, and adding them starts none, so they can't set off another
// call. Never while a turn runs: they wait for it to end.
async function addOutcomes($: EngineInterface): Promise<void> {
  while (outcomes.length && !isTurning) {
    const text = outcomes.shift()!
    $.ui.log(text)
    try {
      await $.session.append({ message: { type: 'user', content: [{ type: 'text', text: outcomeNote(text) }] } })
    } catch (e) {
      $.ui.log(`couldn't add a call's outcome to the conversation: ${e}`, { to: 'debug' })
    }
  }
}

// Gives the briefing to the voice server, which asks the switchboard to ring the phone. A
// newer briefing replaces one still waiting to be offered again.
async function call($: EngineInterface, answer: string): Promise<void> {
  const [first = '', ...rest] = answer.trim().split('\n')
  const hasSubject = /^\W*subject:/i.test(first)
  const subject = hasSubject ? first.replace(/^\W*subject:\s*/i, '').replace(/\W+$/, '').slice(0, 80) : ''
  const briefing = (hasSubject ? rest.join('\n') : answer).trim()
  const cwd = await $.session.cwd()
  const body = {
    session_id: await $.session.id(),
    project: projectOf(cwd),
    cwd,
    subject,
    briefing,
  }
  latest = body
  retry?.timer.cancel()
  await place($, body, RETRY_FOR_MS / RETRY_MS)
}

// Offers a briefing to the voice server. If it can't take it, because it isn't running (it
// may be restarting) or can't reach the switchboard, the briefing is offered again every
// 30 s, for 30 min (`tries` more times); a dim line says so once, and the line under the
// prompt meanwhile.
async function place($: EngineInterface, body: object, tries: number): Promise<void> {
  let failed: string
  isCalling = true
  $.ui.invalidate('ui.render')
  try {
    const response = await voice($, '/calls', body)
    if (response.ok) {
      retry = undefined
      await showHeld($, fieldOf(response.text, 'held_until'))
      return
    }
    failed = errorOf(response.text) ?? `the voice server answered ${response.status}`
    if (response.status < 500) {
      retry = undefined
      $.ui.toast(`Can't call: ${failed}`)
      return
    }
  } catch {
    failed = "the voice server isn't running (ringback-voice serve)"
  } finally {
    isCalling = false
    $.ui.invalidate('ui.render')
  }
  // Switched off, or a newer briefing has taken its place meanwhile.
  if (!isOn || body !== latest) return
  if (tries <= 0) {
    retry = undefined
    $.ui.invalidate('ui.render')
    $.ui.log(`Couldn't call: ${failed}, for 30 min.`)
    return
  }
  if (!retry) $.ui.log(`Can't call yet: ${failed}. Trying again every 30 s for 30 min.`)
  retry = { body, timer: $.clock.after(RETRY_MS, () => void place($, body, tries - 1)) }
  $.ui.invalidate('ui.render')
}

const projectOf = (cwd: string) => cwd.split('/').filter(Boolean).pop() ?? 'Claude'

// Outside your work hours (set in the app) the switchboard holds the call: a dim line says
// when it rings, in this Mac's time, with the date unless it's today.
async function showHeld($: EngineInterface, heldUntil: unknown): Promise<void> {
  const at = typeof heldUntil === 'string' ? new Date(heldUntil) : undefined
  if (!at || Number.isNaN(at.getTime())) return
  const now = new Date(await $.clock.now())
  const two = (n: number) => String(n).padStart(2, '0')
  const time = `${two(at.getHours())}:${two(at.getMinutes())}`
  const isToday = at.toDateString() === now.toDateString()
  const day = `${DAYS[at.getDay()]} ${at.getDate()} ${MONTHS[at.getMonth()]}`
  $.ui.log(`Outside your work hours: the call rings at ${time}${isToday ? '' : ` on ${day}`}.`)
}

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

// The voice server's answer: what a permission or question resolved to, and whether its call
// is held outside your work hours.
type Answer = {
  verdict?: string
  status?: string
  answers?: Record<string, string> | null
  said?: string | null
  held?: boolean
}

// Puts a permission or a question to you on a call, and waits for your answer: long-polls the
// voice server until it has one, you hang up without one (`none`), ringback is switched off,
// the tool call is abandoned or 15 min pass. A call you don't answer leaves it `pending`, so
// the wait goes on, for a call back or the session's next call. Unanswered, it's withdrawn,
// and it's left to the terminal (undefined). A long-poll is a call on `$`, so its wait doesn't
// count against the hook's 10 s; a `$.clock.sleep` would, so there's none. Each poll waits
// 25 s at most, inside the 30 s that one `$.http.fetch` may take. While the call is held
// outside your work hours, the 15 min start again with each poll, so they count from when
// it rings; a question then goes to the terminal at once. While the voice server can't be
// reached, as while it restarts, it's asked again every 5 s within the 15 min; it keeps what
// was asked across a restart, so a poll carries on. A 404 means it's gone.
async function hold(
  $: EngineInterface,
  kind: 'permissions' | 'questions',
  body: { subject: string },
  signal: AbortSignal,
): Promise<Answer | undefined> {
  // Why the terminal asks after all, shown in a dim line.
  let why = 'No answer from the phone'
  let stop: (reason?: string) => void = () => {}
  const stopped = new Promise<Reply>(resolve => {
    stop = reason => {
      if (reason) why = reason
      resolve({})
    }
  })
  const timeUp = () =>
    stop(unreached.has(stop) ? "Couldn't reach the voice server in 15 min" : 'No answer from the phone in 15 min')
  let timer = $.clock.after(HOLD_MS, timeUp)
  const restart = () => {
    timer.cancel()
    timer = $.clock.after(HOLD_MS, timeUp)
  }
  const abandoned = () => stop()
  signal.addEventListener('abort', abandoned)
  if (signal.aborted) stop()
  holds.add(stop)
  $.ui.invalidate('ui.render')
  $.ui.log(`Asking you on the phone: ${body.subject}`)
  const id = kind === 'permissions' ? 'permission' : 'question'
  let path = ''
  let created: Promise<Reply> | undefined
  let answer: Answer | undefined
  // While the voice server can't be reached: says so once, and waits 5 s before asking again;
  // false if the hold was stopped meanwhile.
  const unreachable = async (): Promise<boolean> => {
    if (!unreached.size && !retry) $.ui.log("Can't reach the voice server: asking again every 5 s.")
    unreached.add(stop)
    $.ui.invalidate('ui.render')
    return pause($, REACH_MS, stopped)
  }
  const reachable = () => {
    if (unreached.delete(stop)) $.ui.invalidate('ui.render')
  }
  try {
    path = `/sessions/${await $.session.id()}/${kind}`
    let made: Reply
    do {
      created = reply(voice($, path, body))
      made = await Promise.race([created, stopped])
    } while (made.isUnreached && (await unreachable()))
    reachable()
    const { value, error } = made
    if (!value) {
      if (error) why = `Couldn't ask you on the phone: ${error}`
      return undefined
    }
    if (value.call === 'held') {
      // Outside your work hours, a question goes to the terminal at once, where Remote
      // Control can answer it; the held call's voice is told it waits there.
      if (kind === 'questions') {
        why = 'Outside your work hours'
        return undefined
      }
      restart()
      await showHeld($, value.held_until)
    }
    while (true) {
      const polled = await Promise.race([reply(voice($, `${path}/${value[id]}?wait=25`)), stopped])
      if (polled.isUnreached) {
        if (await unreachable()) continue
        return undefined
      }
      reachable()
      const got: Answer | undefined = polled.value
      if (got?.held) restart()
      if (got?.verdict === 'allow' || got?.verdict === 'deny' || (got?.status === 'answered' && got.answers)) {
        answer = got
        return answer
      }
      if (polled.error) why = `Lost the phone's answer: ${polled.error}`
      // `none`, a failed request, or stopped.
      if (got?.verdict !== 'pending' && got?.status !== 'pending') return undefined
    }
  } finally {
    timer.cancel()
    signal.removeEventListener('abort', abandoned)
    holds.delete(stop)
    unreached.delete(stop)
    $.ui.invalidate('ui.render')
    if (!answer) {
      // Withdrawn where it was made, once it's made: switched off while the voice server was
      // still placing the call, it's withdrawn when that answers.
      const withdraw = () =>
        void created?.then(({ value }) => value && voice($, `${path}/${value[id]}`, undefined, 'DELETE').catch(() => {}))
      // Abandoned, as when you answer the prompt in the terminal first, a call on `$` made now
      // never leaves (seen in a real session); one made from a timer does.
      if (signal.aborted) $.clock.after(0, withdraw)
      else {
        withdraw()
        $.ui.log(`${why}, so it's asked here.`)
      }
    }
  }
}

// What the voice server answered, parsed, or why it didn't: `isUnreached` when it couldn't be
// reached at all, as while it isn't running.
type Reply = { value?: any; error?: string; isUnreached?: boolean }

async function reply(response: Promise<HttpResponse>): Promise<Reply> {
  try {
    const r = await response
    if (r.ok) return { value: JSON.parse(r.text) }
    return { error: errorOf(r.text) ?? `the voice server answered ${r.status}` }
  } catch {
    return { error: "the voice server isn't running (ringback-voice serve)", isUnreached: true }
  }
}

// Waits ms, or until stopped; false if stopped. The wait is a `sleep` on this Mac, a call on
// `$`, so it doesn't count against the hook's 10 s, as a `$.clock.sleep` would.
async function pause($: EngineInterface, ms: number, stopped: Promise<Reply>): Promise<boolean> {
  let isStopped = false
  await Promise.race([
    $.process.run(['/bin/sleep', String(ms / 1000)]).catch(() => $.clock.sleep(ms)),
    stopped.then(() => (isStopped = true)),
  ])
  return !isStopped
}

// A permission as the voice puts it to you: what the tool would do, and what the session was
// doing.
async function permission($: EngineInterface, tool: string, input: unknown) {
  const args = (input ?? {}) as Record<string, unknown>
  const path = [args.file_path, args.notebook_path].find(p => typeof p === 'string') as string | undefined
  const file = path?.split('/').pop() || 'a file'
  const subjects: Record<string, string> = {
    Bash: 'Permission to run a shell command',
    Edit: `Permission to edit ${file}`,
    NotebookEdit: `Permission to edit ${file}`,
    Write: `Permission to write ${file}`,
    Read: `Permission to read ${file}`,
  }
  const json = JSON.stringify(input, null, 2) ?? ''
  return {
    tool,
    // The tool's own, where it has one (Bash, Agent): a permission request carries no reason.
    description: typeof args.description === 'string' ? args.description : '',
    input: json.length > 2000 ? `${json.slice(0, 2000)}…` : json,
    ...(await doing($)),
    subject: (subjects[tool] ?? `Permission to use ${tool.replace(/^mcp__/, '').replace(/__/g, ' ')}`).slice(0, 80),
  }
}

// The session's questions as the voice puts them to you, with what it was doing.
async function questions(
  $: EngineInterface,
  asked: readonly { question: string; header: string; options?: readonly { label: string; description?: string }[]; multiSelect: boolean }[],
) {
  return {
    questions: asked.map(q => ({
      question: q.question,
      header: q.header,
      options: (q.options ?? []).map(o => ({ label: o.label, description: o.description ?? '' })),
      multiSelect: q.multiSelect,
    })),
    ...(await doing($)),
    subject: (asked.length === 1 ? `Question: ${asked[0]?.header}` : `${asked.length} questions: ${asked.map(q => q.header).join(', ')}`).slice(0, 80),
  }
}

// What the session was doing, from its latest text (the end of it, if long), and where.
async function doing($: EngineInterface) {
  let context = ''
  try {
    context = (await $.session.messages()).findLast(m => m.role === 'assistant' && m.text.trim())?.text.trim() ?? ''
  } catch {
    // No context: the voice still has the request.
  }
  const cwd = await $.session.cwd()
  return {
    context: context.length > 1500 ? `…${context.slice(-1500)}` : context,
    project: projectOf(cwd),
    cwd,
  }
}

function errorOf(text: string): string | undefined {
  return fieldOf(text, 'error') as string | undefined
}

// One field of the voice server's JSON answer, if it has it.
function fieldOf(text: string, key: string): unknown {
  try {
    return JSON.parse(text)?.[key]
  } catch {
    return undefined
  }
}
