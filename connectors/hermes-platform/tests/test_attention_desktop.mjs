// node --experimental-vm-modules tests/test_attention_desktop.mjs
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { SourceTextModule, SyntheticModule, createContext } from 'node:vm'
import test from 'node:test'

const lockQueue = new Map()
const browserLocks = { request(key, callback) {
  const next = (lockQueue.get(key) || Promise.resolve()).then(callback)
  lockQueue.set(key, next.catch(() => {}))
  return next
} }
const hostStub = {}
const context = createContext({ console, setInterval, clearInterval, Date, Map, Set, JSON, navigator: { locks: browserLocks } })
const sdk = new SyntheticModule(['host', 'ROUTES_AREA', 'SIDEBAR_NAV_AREA'], function () {
  this.setExport('host', hostStub)
  this.setExport('ROUTES_AREA', 'routes')
  this.setExport('SIDEBAR_NAV_AREA', 'sidebar')
}, { context })
const react = new SyntheticModule(['createElement', 'useSyncExternalStore'], function () {
  this.setExport('createElement', (type, props, ...children) => typeof type === 'function' ? type(props || {}) : ({ type, props: props || {}, children }))
  this.setExport('useSyncExternalStore', (_, getSnapshot) => getSnapshot())
}, { context })
const source = await readFile(new URL('../hermes_platform_agent_comm/companion/desktop/plugin.js', import.meta.url), 'utf8')
const plugin = new SourceTextModule(source, { context })
await plugin.link(name => name === '@hermes/plugin-sdk' ? sdk : react)
await plugin.evaluate()
const { createAttentionController } = plugin.namespace
const item = (id, revision = 1, state = 'open') => ({ attention_id: id, kind: 'owner_decision_required', revision, state, title: '待办', safe_summary: '请查看', updated_at: revision, target: { kind: 'approval', id } })
const page = (items, cursor, extra = {}) => ({ schema: 'agent-comm-attention/v1', items, cursor, has_more: false, owner_key: 'owner-a', available: true, ...extra })
function controller(rest, options = {}) {
  const attempts = []
  const persisted = new Map()
  const value = createAttentionController({ rest, scope: () => 'profile-a', notify: (...args) => attempts.push(args),
    persist: (k, v) => persisted.set(k, v), readPersist: k => persisted.get(k), clock: () => 100000, ...options })
  return { value, attempts }
}

test('pagination produces one digest; replay and resolution never notify again', async () => {
  const queue = [page([item('a')], 1, { has_more: true }), page([item('b', 2)], 2), page([], 2), page([item('a', 3, 'resolved')], 3)]
  const paths = []
  const { value, attempts } = controller(async path => { paths.push(path); return queue.shift() })
  await value.poll()
  assert.equal(value.getSnapshot().items.length, 2)
  assert.equal(attempts.length, 1)
  assert.equal(attempts[0][0], 2)
  assert.equal(paths[1], '/attention?after=1&limit=100')
  await value.poll()
  await value.poll()
  assert.equal(attempts.length, 1)
  assert.equal(value.getSnapshot().items.find(x => x.attention_id === 'a').state, 'resolved')
})

test('failed poll keeps durable projection and retry notifies newly observed item', async () => {
  let calls = 0
  const { value, attempts } = controller(async () => { calls += 1; if (calls === 2) throw new Error('offline'); return page([item('a', calls)], calls) })
  await value.poll()
  await value.poll()
  assert.equal(value.getSnapshot().items.length, 1)
  assert.ok(value.getSnapshot().error)
  await value.poll()
  assert.equal(value.getSnapshot().error, '')
  assert.equal(attempts.length, 2)
})

test('a large update preserves notification candidates across bounded polls and a page failure', async () => {
  let calls = 0
  const { value, attempts } = controller(async () => {
    calls += 1
    if (calls === 1) return page([], 0)
    if (calls === 12) throw new Error('temporary failure between pages')
    const revision = calls === 13 ? 11 : calls - 1
    return page([{ ...item(`ordinary-${revision}`, revision), kind: 'peer_message_received' }], revision, { has_more: revision < 11 })
  })
  await value.poll()
  await value.poll()
  assert.equal(attempts.length, 0)
  assert.equal(value.getSnapshot().loading, true)
  await value.poll()
  assert.ok(value.getSnapshot().error)
  await value.poll()
  assert.equal(attempts.length, 1)
  assert.equal(attempts[0][0], 11)
})

test('response from a previous profile is discarded without leaking or notifying', async () => {
  let selected = 'a'
  let finish
  const { value, attempts } = controller(() => new Promise(resolve => { finish = resolve }), { scope: () => selected })
  const polling = value.poll()
  selected = 'b'
  value.reset()
  finish(page([item('secret-a')], 1))
  await polling
  assert.equal(value.getSnapshot().items.length, 0)
  assert.equal(value.getSnapshot().scope, 'b')
  assert.equal(attempts.length, 0)
})

test('unknown contract and nonadvancing cursor fail closed', async () => {
  for (const response of [{ ...page([], 0), schema: 'future' }, page([], 0, { has_more: true })]) {
    const { value, attempts } = controller(async () => response)
    await value.poll()
    assert.ok(value.getSnapshot().error)
    assert.equal(attempts.length, 0)
  }
})

test('expired items remain visible in history and never ask through notifications', async () => {
  const expired = { ...item('old'), expires_at: 99 }
  const { value, attempts } = controller(async () => page([expired], 1))
  await value.poll()
  assert.equal(value.getSnapshot().items.length, 1)
  assert.equal(value.isOpen(expired), false)
  assert.equal(attempts.length, 0)
})

test('disposal prevents a late request from notifying', async () => {
  let finish
  const { value, attempts } = controller(() => new Promise(resolve => { finish = resolve }))
  const polling = value.poll()
  value.dispose()
  finish(page([item('a')], 1))
  await polling
  assert.equal(attempts.length, 0)
})

test('historical ordinary messages are quiet; read is UI-only and never resolves a decision', async () => {
  const ordinary = { ...item('old'), kind: 'peer_message_received' }
  const decision = item('decision', 2)
  const { value, attempts } = controller(async () => page([ordinary, decision], 2))
  await value.poll()
  assert.equal(attempts[0][0], 1)
  assert.equal(value.isPending(ordinary), false)
  assert.equal(value.isUnread(ordinary), true)
  assert.equal(value.getSnapshot().items.filter(value.isPending).length, 1)
  value.markRead(ordinary)
  assert.equal(value.isPending(ordinary), false)
  assert.equal(value.isUnread(ordinary), false)
  assert.equal(value.getSnapshot().items.find(x => x.attention_id === 'old').state, 'open')
  value.markRead(decision)
  assert.equal(value.isPending(decision), true)
  assert.equal(value.isUnread(decision), false)
  assert.equal(value.getSnapshot().items.filter(value.isPending).length, 1)
})

test('persisted attempt watermark suppresses replay after controller restart', async () => {
  const storage = new Map()
  const options = { persist: (key, value) => storage.set(key, value), readPersist: key => storage.get(key) }
  const first = controller(async () => page([item('a')], 1), options)
  await first.value.poll()
  assert.equal(first.attempts.length, 1)
  const restarted = controller(async () => page([item('a')], 1), options)
  await restarted.value.poll()
  assert.equal(restarted.attempts.length, 0)
  assert.equal(restarted.value.getSnapshot().items.length, 1)
})

test('two actual companion registrations share one claim and notification is only a safe summary', async () => {
  const native = [], toasts = [], registrations = [], disposers = [], navigations = []
  const values = new Map()
  const atom = value => ({ get: () => value, listen: () => () => {} })
  Object.assign(hostStub, { state: { profile: atom('work'), connectionId: atom('local') },
    notify: value => toasts.push(value), navigate: path => navigations.push(path),
    openSession: () => { throw new Error('Opening a session must require a user click') },
    newChat: () => { throw new Error('Opening a chat must require a user click') } })
  const ctx = () => ({ rest: async () => page([{ ...item('new'), safe_summary: 'PRIVATE PAYLOAD' }], 1),
    storage: { get: (k, fallback) => values.get(k) || fallback, set: (k, v) => values.set(k, v) },
    os: { notify: value => native.push(value) },
    registerMany: rows => registrations.push(rows), onDispose: fn => disposers.push(fn) })
  plugin.namespace.default.register(ctx())
  plugin.namespace.default.register(ctx())
  await new Promise(resolve => setImmediate(resolve))
  try {
    assert.equal(native.length, 1)
    assert.equal(toasts.length, 1)
    assert.equal(native[0].activate, '/agent-comm-attention')
    assert.ok(!JSON.stringify(native[0]).includes('PRIVATE'))
    const pageContribution = registrations[0].find(row => row.id === 'page')
    const tree = pageContribution.render()
    assert.equal(tree.type, 'main')
    assert.ok(JSON.stringify(tree).includes('协作待办'))
    toasts[0].action.onClick()
    assert.deepEqual(navigations, ['/agent-comm-attention'])
  } finally { for (const dispose of disposers) dispose() }
})

test('explicit recovery click refreshes current state, copies instructions and opens the same profile only', async () => {
  const findButton = tree => {
    if (!tree || typeof tree !== 'object') return null
    if (tree.type === 'button' && tree.children.includes('复制指令并打开原生对话')) return tree
    for (const child of tree.children || []) { const found = findButton(child); if (found) return found }
    return null
  }
  for (const stale of [false, true]) {
    const calls = [], disposers = [], registrations = []
    const values = new Map()
    const atom = value => ({ get: () => value, listen: () => () => {} })
    Object.assign(hostStub, { state: { profile: atom('work'), connectionId: atom('local') },
      notify: () => {}, navigate: () => {},
      openSession: (...args) => calls.push(['openSession', ...args]), newChat: (...args) => calls.push(['newChat', ...args]) })
    let reads = 0
    const record = { ...item('recover'), resume: { stored_session_id: 'native-original', instruction: '读取 approval_id 并在原生问题卡确认' } }
    const ctx = { rest: async () => { reads += 1; return page([reads > 1 && stale ? { ...record, state: 'resolved', revision: 2 } : record], reads > 1 && stale ? 2 : 1) },
      storage: { get: (k, fallback) => values.get(k) || fallback, set: (k, v) => values.set(k, v) },
      os: { notify: () => {}, writeClipboard: text => calls.push(['clipboard', text]) },
      registerMany: rows => registrations.push(rows), onDispose: fn => disposers.push(fn) }
    plugin.namespace.default.register(ctx)
    await new Promise(resolve => setImmediate(resolve))
    try {
      assert.equal(calls.length, 0)
      const tree = registrations[0].find(row => row.id === 'page').render()
      const button = findButton(tree)
      assert.ok(button)
      button.props.onClick()
      await new Promise(resolve => setImmediate(resolve))
      assert.equal(reads, 2)
      if (stale) assert.equal(calls.length, 0)
      else {
        assert.equal(calls.length, 2)
        assert.equal(calls[0][0], 'clipboard')
        assert.equal(calls[1][0], 'openSession')
        assert.equal(calls[1][1], 'native-original')
        assert.equal(calls[1][2].profile, 'work')
      }
    } finally { for (const dispose of disposers) dispose() }
  }
})
