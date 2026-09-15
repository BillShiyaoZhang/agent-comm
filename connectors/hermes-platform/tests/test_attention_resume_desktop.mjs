// node --experimental-vm-modules --test tests/test_attention_resume_desktop.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { SourceTextModule, SyntheticModule, createContext } from 'node:vm'
import test from 'node:test'

const context = createContext({ console, Date, Map, Set, JSON })
const sdk = new SyntheticModule(['host', 'ROUTES_AREA', 'SIDEBAR_NAV_AREA'], function () {
  this.setExport('host', {}); this.setExport('ROUTES_AREA', 'routes'); this.setExport('SIDEBAR_NAV_AREA', 'sidebar')
}, { context })
const react = new SyntheticModule(['createElement', 'useSyncExternalStore'], function () {
  this.setExport('createElement', () => null); this.setExport('useSyncExternalStore', () => null)
}, { context })
const source = await readFile(new URL('../hermes_platform_agent_comm/companion/desktop/plugin.js', import.meta.url), 'utf8')
const plugin = new SourceTextModule(source, { context })
await plugin.link(name => name === '@hermes/plugin-sdk' ? sdk : react)
await plugin.evaluate()
const { createResumeController, handlingSessionTitle } = plugin.namespace
const record = { attention_id: 'attention-one', revision: 1, state: 'open', task_id: 'task-one', title: '讨论排期',
  expires_at: 200, details: { can_resume: true, context_summary: '与张三讨论排期', question: '是否同意当前具体方案？' } }
const atom = get => ({ get })
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }

function harness(options = {}) {
  const calls = [], storage = new Map(), queues = new Map()
  const state = options.sharedState || { selected: 'scope-a', owner: 'owner-a', profile: 'work', connection: 'local', gateway: 'open',
    binding: options.binding || null, submission: 'ready', resume: 'resume-one', created: 0, releases: 0, ...options.state }
  const rest = async (path, request) => {
    calls.push([path, request.body])
    if (options.onRest) { const result = await options.onRest(path, request, state); if (result !== undefined) return result }
    if (path === '/attention/prepare-resume') return {
      schema: 'agent-comm-resume/v1', owner_key: state.owner, resume_id: state.resume, item: { ...record, ...options.record },
      stored_session_id: state.binding, session_state: state.binding ? 'available' : options.sessionState || 'none',
      submission_state: state.submission, instruction: '请读取这项待办的当前背景并展示原生问题；不要发送业务消息或代我承诺。'
    }
    if (path === '/attention/bind-session') {
      state.binding ||= request.body.stored_session_id
      return { resume_id: state.resume, stored_session_id: state.binding, session_state: 'available' }
    }
    if (path === '/attention/claim-submit') {
      assert.equal(request.body.stored_session_id, state.binding)
      if (state.submission !== 'ready') return { claimed: false, submission_state: state.submission }
      state.submission = 'submitting'
      return { claimed: true, submission_state: state.submission, claim_token: 'claim-token' }
    }
    if (path === '/attention/finish-submit') {
      assert.equal(request.body.claim_token, 'claim-token')
      state.submission = request.body.outcome
      return { resume_id: state.resume, submission_state: state.submission }
    }
    assert.fail(`Unexpected backend operation ${path}`)
  }
  const host = {
    state: { profile: atom(() => state.profile), connectionId: atom(() => state.connection), gateway: atom(() => state.gateway) },
    profileRoutes: async () => options.routes || [{ connectionId: 'local', mode: 'local', profile: 'work', targetProfile: 'work' }],
    retainProfile: async route => { calls.push(['retain', route]); return () => { state.releases++ } },
    openSession: async (id, opts) => {
      calls.push(['openSession', id, opts]); if (options.onOpen) await options.onOpen(id, state)
    },
    requestProfile: async (route, method, params, timeout) => {
      calls.push([method, params, route, timeout])
      if (options.onRequest) { const result = await options.onRequest(method, params, state); if (result !== undefined) return result }
      assert.equal(route.connectionId, 'local'); assert.equal(params.profile, 'work')
      if (method === 'session.create') { state.created++; return { stored_session_id: `stored-${state.created}`, session_id: `runtime-${state.created}` } }
      if (method === 'session.title') return { status: 'ok' }
      if (method === 'session.resume') return { stored_session_id: params.session_id, session_id: `live-${params.session_id}` }
      if (method === 'prompt.submit') return { status: 'streaming' }
      assert.fail(`Unexpected gateway operation ${method}`)
    },
    newChat: () => assert.fail('A draft without a stable stored ID must never be used'),
    notify: () => assert.fail('No generic toast may hide a processing failure')
  }
  const lock = options.lock === false ? undefined : async (key, callback) => {
    const next = (queues.get(key) || Promise.resolve()).then(callback)
    queues.set(key, next.catch(() => {})); return next
  }
  const make = () => createResumeController({ rest, host, scope: () => state.selected, readAttention: () => ({ owner: state.owner }), lock,
    persist: (key, value) => storage.set(key, value), readPersist: key => storage.get(key), clock: () => 100000 })
  const value = make()
  return { value, make, calls, state, storage, start: data => value.start({ ...record, ...data }),
    count: name => calls.filter(c => c[0] === name).length,
    status: () => value.getSnapshot().entries[record.attention_id]?.phase }
}

test('explicit handling creates, persists and binds a native session before sending the bounded background once', async () => {
  const h = harness()
  assert.equal(h.calls.length, 0, 'Construction/polling cannot create sessions or submit')
  await h.start()
  assert.equal(h.status(), 'submitted')
  assert.equal(h.state.releases, 1)
  assert.deepEqual(h.calls.map(c => c[0]), ['retain', '/attention/prepare-resume', 'session.create', 'session.title',
    '/attention/bind-session', 'openSession', 'session.resume', '/attention/claim-submit', 'prompt.submit', '/attention/finish-submit'])
  const submit = h.calls.find(c => c[0] === 'prompt.submit')[1]
  assert.equal(submit.queued, true, 'An existing running turn must never be steered by the handling request')
  assert.equal(submit.session_id, 'live-stored-1')
  assert.match(submit.text, /不要发送业务消息/)
  assert.equal(h.value.getSnapshot().entries[record.attention_id].details.question, record.details.question)
  assert.equal(h.calls.find(c => c[0] === 'session.create')[1].title, undefined, 'An untitled lazy draft cannot reserve a shared generic title')
  assert.equal(h.calls.find(c => c[0] === 'session.title')[1].title, handlingSessionTitle(record.title, 'stored-1'))
})

test('same-label handling titles retain the full native identity within the Hermes title limit', () => {
  const first = handlingSessionTitle('协作委托需要你确认'.repeat(30), '20260915_174037_d3b406')
  const second = handlingSessionTitle('协作委托需要你确认'.repeat(30), '20260915_174037_a91a3f')
  assert.ok(first.length <= 100)
  assert.notEqual(first, second)
  assert.ok(first.endsWith('20260915_174037_d3b406'), 'Truncate the label before appending the identity')
  assert.equal(first, handlingSessionTitle('协作委托需要你确认'.repeat(30), '20260915_174037_d3b406'))
})

test('independent windows racing the same resume use unique draft titles and the bind winner submits once', async () => {
  const titled = new Map(), bothTitled = deferred()
  const onRequest = async (method, params) => {
    if (method !== 'session.title') return undefined
    assert.ok(!titled.has(params.title), 'The real Hermes profile enforces unique titles')
    titled.set(params.title, params.session_id)
    if (titled.size === 2) bothTitled.resolve()
    await bothTitled.promise
  }
  const first = harness({ onRequest })
  const second = harness({ onRequest, sharedState: first.state })
  await Promise.all([first.start(), second.start()])
  assert.equal(titled.size, 2)
  assert.equal(first.state.created, 2, 'Independent origins need not share WebLocks or draft storage')
  assert.equal(first.count('prompt.submit') + second.count('prompt.submit'), 1)
  assert.equal(first.calls.find(c => c[0] === 'openSession')[1], first.state.binding)
  assert.equal(second.calls.find(c => c[0] === 'openSession')[1], first.state.binding)
})

test('a failed title write keeps the same draft and deterministic title for the next explicit retry', async () => {
  let fail = true
  const h = harness({ onRequest: async method => {
    if (method === 'session.title' && fail) { fail = false; throw Object.assign(new Error('PRIVATE duplicate title'), { code: 4022 }) }
  } })
  await h.start()
  const first = h.value.getSnapshot().entries[record.attention_id]
  assert.equal(first.phase, 'failed')
  assert.equal(first.failureStage, 'titling')
  assert.match(first.message, /保存处理会话名称失败/)
  assert.ok(!JSON.stringify(first).includes('PRIVATE'))
  assert.equal(h.count('/attention/bind-session'), 0)
  await h.start()
  assert.equal(h.status(), 'submitted')
  assert.equal(h.count('session.create'), 1)
  assert.equal(h.count('prompt.submit'), 1)
  assert.equal(new Set(h.calls.filter(c => c[0] === 'session.title').map(c => c[1].title)).size, 1)
  assert.equal(h.value.getSnapshot().entries[record.attention_id].failureStage, null)
})

test('failures identify the safe processing stage without exposing native error bodies', async () => {
  const cases = [
    ['creating', { onRequest: async method => { if (method === 'session.create') throw new Error('PRIVATE backend') } }],
    ['binding', { onRest: async path => { if (path === '/attention/bind-session') throw new Error('PRIVATE backend') } }],
    ['opening', { binding: 'original-session', onOpen: async () => { throw new Error('PRIVATE backend') } }],
    ['verifying_session', { binding: 'original-session', onRequest: async method => { if (method === 'session.resume') throw new Error('PRIVATE backend') } }],
    ['claiming', { binding: 'original-session', onRest: async path => { if (path === '/attention/claim-submit') throw new Error('PRIVATE backend') } }]
  ]
  for (const [stage, options] of cases) {
    const h = harness(options)
    await h.start()
    const entry = h.value.getSnapshot().entries[record.attention_id]
    assert.equal(entry.phase, 'failed')
    assert.equal(entry.failureStage, stage)
    assert.ok(!JSON.stringify(entry).includes('PRIVATE'))
    assert.equal(h.count('prompt.submit'), 0)
  }
})

test('available original session is hydrated on its exact route and no new draft is created', async () => {
  const h = harness({ binding: 'original-session' })
  await h.start()
  assert.equal(h.count('session.create'), 0)
  const opened = h.calls.find(c => c[0] === 'openSession')
  assert.equal(opened[1], 'original-session')
  assert.equal(opened[2].route.connectionId, 'local')
  assert.equal(opened[2].awaitHydration, true)
  assert.equal(h.count('prompt.submit'), 1)
})

test('same-click overlap and two windows share one binding and one prompt; later clicks only reopen', async () => {
  const h = harness()
  const second = h.make()
  const firstClick = h.start()
  assert.equal(h.start(), firstClick)
  await Promise.all([firstClick, second.start(record)])
  await h.start()
  assert.equal(h.count('session.create'), 1)
  assert.equal(h.count('prompt.submit'), 1)
  assert.equal(h.status(), 'reopened')
  assert.equal(h.count('openSession'), 3)
})

test('CAS binding from another device wins and the orphan draft is never submitted', async () => {
  const h = harness({ onRest: async (path, _request, state) => {
    if (path === '/attention/bind-session') state.binding = 'other-device-winner'
  } })
  await h.start()
  assert.equal(h.calls.find(c => c[0] === 'openSession')[1], 'other-device-winner')
  assert.equal(h.calls.find(c => c[0] === 'prompt.submit')[1].session_id, 'live-other-device-winner')
})

test('a definitely missing old session can create a replacement for the backend next binding generation', async () => {
  const h = harness({ sessionState: 'missing', state: { resume: 'resume-generation-two' } })
  await h.start()
  assert.equal(h.status(), 'submitted')
  assert.equal(h.count('session.create'), 1)
  assert.equal(h.calls.find(c => c[0] === '/attention/bind-session')[1].resume_id, 'resume-generation-two')
})

test('failed binding response recovers the persisted draft instead of creating another session', async () => {
  let fail = true
  const h = harness({ onRest: async path => {
    if (path === '/attention/bind-session' && fail) { fail = false; throw new Error('network timeout') }
  } })
  await h.start()
  assert.equal(h.status(), 'failed')
  assert.equal(h.count('prompt.submit'), 0)
  await h.start()
  assert.equal(h.status(), 'submitted')
  assert.equal(h.count('session.create'), 1)
})

test('only the real stored-session-missing RPC code can replace an unbound draft after a retry', async () => {
  for (const code of [4007, 4001, 5000]) {
    let failBinding = true, missing = false
    const h = harness({ onRest: async path => {
      if (path === '/attention/bind-session' && failBinding) { failBinding = false; throw new Error('lost response') }
    }, onRequest: async method => {
      if (method === 'session.resume' && missing) { missing = false; throw Object.assign(new Error('private RPC body'), { code }) }
    } })
    await h.start(); missing = true
    await h.start()
    assert.equal(h.count('session.create'), code === 4007 ? 2 : 1)
    assert.equal(h.count('prompt.submit'), code === 4007 ? 1 : 0)
  }
})

test('hydration failure never falls back to a new session or submits into an unverified view', async () => {
  const h = harness({ binding: 'original-session', onOpen: async () => { throw new Error('hydration timeout') } })
  await h.start()
  assert.equal(h.status(), 'failed')
  assert.equal(h.count('session.create'), 0)
  assert.equal(h.count('prompt.submit'), 0)
  assert.equal(h.count('/attention/claim-submit'), 0)
})

test('expired, changed or unavailable source fails before creating or submitting a conversation', async () => {
  for (const options of [{ record: { state: 'expired' } }, { record: { expires_at: 99 } },
    { onRest: async () => { throw Object.assign(new Error('PRIVATE SOURCE DATA'), { status: 409 }) } },
    { onRest: async () => { throw Object.assign(new Error('PRIVATE SOURCE DATA'), { status: 503 }) } }]) {
    const h = harness(options)
    await h.start()
    assert.equal(h.count('session.create'), 0)
    assert.equal(h.count('prompt.submit'), 0)
    assert.ok(['expired', 'changed', 'failed'].includes(h.status()))
    assert.ok(!JSON.stringify(h.value.getSnapshot()).includes('PRIVATE'))
  }
})

test('Hermes HTTP statusCode and Electron serialized 409 both yield the safe changed-state explanation', async () => {
  for (const error of [Object.assign(new Error('PRIVATE'), { statusCode: 409 }),
    new Error("Error invoking remote method 'hermes:api': Error: 409: PRIVATE BODY")]) {
    const h = harness({ onRest: async () => { throw error } })
    await h.start()
    assert.equal(h.status(), 'changed')
    assert.ok(!JSON.stringify(h.value.getSnapshot()).includes('PRIVATE'))
  }
})

test('offline connection, missing locks or ambiguous profile routes fail closed', async () => {
  for (const options of [{ state: { gateway: 'connecting' } }, { lock: false }, { routes: [
    { connectionId: 'local', profile: 'work', targetProfile: 'work' },
    { connectionId: 'local', profile: 'work', targetProfile: 'other' }
  ] }, { routes: [{ connectionId: 'somebody-else', profile: 'work', targetProfile: 'work' }] }]) {
    const h = harness(options)
    await h.start()
    assert.equal(h.count('/attention/prepare-resume'), 0)
    assert.equal(h.count('prompt.submit'), 0)
    assert.ok(['failed', 'unsupported'].includes(h.status()))
  }
})

test('an immediately rejected click can retry after the gateway reconnects', async () => {
  const h = harness({ state: { gateway: 'connecting' } })
  await h.start()
  assert.equal(h.status(), 'failed')
  h.state.gateway = 'open'
  await h.start()
  assert.equal(h.status(), 'submitted')
  assert.equal(h.count('prompt.submit'), 1)
})

test('switching connection during prepare discards context and prevents a session or submission', async () => {
  const wait = deferred()
  const h = harness({ onRest: async path => { if (path === '/attention/prepare-resume') await wait.promise } })
  const running = h.start()
  await new Promise(resolve => setImmediate(resolve))
  h.state.selected = 'scope-b'; h.value.reset(); wait.resolve()
  await running
  assert.equal(h.count('session.create'), 0)
  assert.equal(h.count('prompt.submit'), 0)
  assert.equal(Object.keys(h.value.getSnapshot().entries).length, 0)
  assert.equal(h.state.releases, 1)
})

test('switching owner while hydrating never transfers the prior owner background', async () => {
  const h = harness({ binding: 'original-session', onOpen: async (_id, state) => { state.owner = 'owner-b' } })
  await h.start()
  assert.equal(h.count('prompt.submit'), 0)
  assert.equal(h.count('/attention/claim-submit'), 0)
})

test('a stale source rejected at claim cannot send a prepared prompt', async () => {
  const h = harness({ onRest: async path => { if (path === '/attention/claim-submit') throw Object.assign(new Error('changed'), { status: 409 }) } })
  await h.start()
  assert.equal(h.count('openSession'), 1)
  assert.equal(h.count('prompt.submit'), 0)
  assert.equal(h.status(), 'changed')
})

test('lost submit response is recorded as uncertain and never resent on repeated clicks', async () => {
  const h = harness({ onRequest: async method => { if (method === 'prompt.submit') throw new Error('lost result') } })
  await h.start()
  assert.equal(h.status(), 'uncertain')
  assert.equal(h.state.submission, 'uncertain')
  await h.start()
  assert.equal(h.count('prompt.submit'), 1)
  assert.equal(h.count('openSession'), 2)
})

test('lost finish response keeps a submitted claim non-retryable, including from a second window', async () => {
  const h = harness({ onRest: async path => { if (path === '/attention/finish-submit') throw new Error('lost result') } })
  await h.start()
  assert.equal(h.status(), 'uncertain')
  assert.equal(h.state.submission, 'submitting')
  await h.make().start(record)
  assert.equal(h.count('prompt.submit'), 1)
})

test('queue acceptance is described as queued, without claiming a question is already displayed', async () => {
  const h = harness({ onRequest: async method => method === 'prompt.submit' ? { status: 'queued' } : undefined })
  await h.start()
  assert.equal(h.status(), 'queued')
  assert.equal(h.state.submission, 'submitted')
  assert.match(h.value.getSnapshot().entries[record.attention_id].message, /当前回合结束后/)
})

test('invalid owner or binding in the response is never accepted as a native handling destination', async () => {
  for (const onRest of [
    async path => path === '/attention/prepare-resume' ? { schema: 'agent-comm-resume/v1', owner_key: 'owner-other' } : undefined,
    async path => path === '/attention/bind-session' ? { stored_session_id: '../../foreign-session' } : undefined
  ]) {
    const h = harness({ onRest })
    await h.start()
    assert.equal(h.status(), 'changed')
    assert.equal(h.count('openSession'), 0)
    assert.equal(h.count('prompt.submit'), 0)
  }
})
