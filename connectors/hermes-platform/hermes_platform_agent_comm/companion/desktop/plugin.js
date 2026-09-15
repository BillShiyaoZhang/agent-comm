import { host, ROUTES_AREA, SIDEBAR_NAV_AREA } from '@hermes/plugin-sdk'
import { createElement as h, useSyncExternalStore } from 'react'

const PAGE = '/agent-comm-attention'
const OPEN = new Set(['open'])
const ACTIONABLE = new Set(['owner_decision_required', 'new_collaboration_request', 'needs_recovery', 'needs_response'])
const ERROR_NAMES = new Set(['Error', 'TypeError', 'SecurityError', 'NotAllowedError', 'AbortError', 'InvalidStateError', 'NotSupportedError', 'QuotaExceededError'])
const errorName = error => ERROR_NAMES.has(error?.name) ? error.name : 'UnknownError'
const emptyDiagnostics = () => ({ webLocks: 'unknown', secureContext: null, claimState: 'idle', candidates: 0, claimed: 0,
  inAppCall: 'idle', osCall: 'idle', osAttempts: 0, osAttemptAt: null, hidden: null, focused: null,
  lastErrorStage: '', lastErrorName: '', lastErrorAt: null })
const diagnosticText = d => [
  `Web Locks: ${d.webLocks} · secureContext: ${String(d.secureContext)}`,
  `claim: ${d.claimState} · 候选 ${d.candidates} · 本轮取得 ${d.claimed}`,
  `站内调用: ${d.inAppCall} · OS 调用: ${d.osCall} · 尝试 ${d.osAttempts} 次`,
  `最近 OS 尝试: ${d.osAttemptAt ? new Date(d.osAttemptAt).toLocaleTimeString() : '无'} · hidden: ${String(d.hidden)} · focused: ${String(d.focused)}`,
  `最近错误: ${d.lastErrorStage ? `${d.lastErrorStage} / ${d.lastErrorName} · ${new Date(d.lastErrorAt).toLocaleTimeString()}` : '无'}`,
  'OS 调用返回不代表已经显示；宿主通知偏好、前后台、启动静默和节流仍然生效。'
].join('\n')

// Exported for behavioral tests with a fake host. The controller never calls
// prompt.submit, conversation.send, confirm, or any model/tool execution door.
export function createAttentionController({ rest, scope, notify, persist, readPersist, claim, clock = Date.now }) {
  let snapshot = { items: [], scope: scope(), owner: null, loading: true, error: '', available: true, updatedAt: null, syncRevision: 0, diagnostics: emptyDiagnostics() }
  let cursor = 0
  let generation = 0
  let busy = false
  let disposed = false
  let baseline = true
  const records = new Map()
  const changesToNotify = new Map()
  const listeners = new Set()
  const publish = changes => {
    snapshot = { ...snapshot, ...changes }
    for (const listener of listeners) listener()
  }
  const keyFor = () => JSON.stringify([snapshot.scope, snapshot.owner])
  const visible = () => [...records.values()].sort((a, b) => b.updated_at - a.updated_at)
  const isOpen = item => OPEN.has(item.state) && (!item.expires_at || item.expires_at * 1000 > clock())
  const isRead = item => ((readPersist(`seen:${keyFor()}`) || {})[item.attention_id] || 0) >= item.revision
  const isUnread = item => isOpen(item) && !isRead(item)
  const isPending = item => isOpen(item) && ACTIONABLE.has(item.kind)
  const claimAttempts = claim || (async (items, key) => {
    const previous = readPersist(key) || {}
    const fresh = items.filter(item => (previous[item.attention_id] || 0) < item.revision)
    for (const item of fresh) previous[item.attention_id] = item.revision
    persist(key, previous)
    return fresh
  })
  function reset() {
    generation += 1
    cursor = 0
    baseline = true
    records.clear()
    changesToNotify.clear()
    publish({ items: [], scope: scope(), owner: null, loading: true, error: '', available: true, updatedAt: null, diagnostics: emptyDiagnostics() })
  }
  async function poll() {
    if (disposed) return
    if (scope() !== snapshot.scope) reset()
    if (busy) return
    busy = true
    const turn = generation
    const expectedScope = snapshot.scope
    // Only fixed scalar fields reach diagnostics. Never copy item content,
    // scope keys, lock names, exception messages or stacks into this view.
    const diagnose = changes => {
      if (!disposed && turn === generation && expectedScope === scope()) {
        publish({ diagnostics: { ...snapshot.diagnostics, ...(typeof changes === 'function' ? changes(snapshot.diagnostics) : changes) } })
      }
    }
    try {
      let more = false
      for (let page = 0; page < 10; page += 1) {
        const result = await rest(`/attention?after=${cursor}&limit=100`, { timeoutMs: 10000 })
        if (disposed || turn !== generation || expectedScope !== scope()) return
        if (result.schema !== 'agent-comm-attention/v1' || !Array.isArray(result.items)
          || !Number.isSafeInteger(result.cursor) || result.cursor < cursor || typeof result.has_more !== 'boolean') {
          throw new Error('Unsupported attention response')
        }
        if (result.available === false) {
          records.clear()
          changesToNotify.clear()
          cursor = 0
          baseline = true
          publish({ items: [], loading: false, available: false, error: '此 profile 尚未启用个人协作。' })
          return
        }
        if (typeof result.owner_key !== 'string' || !result.owner_key) throw new Error('Missing owner scope')
        if (snapshot.owner && snapshot.owner !== result.owner_key) {
          reset()
          return
        }
        publish({ owner: result.owner_key })
        for (const item of result.items) {
          if (!item || typeof item.attention_id !== 'string' || !Number.isSafeInteger(item.revision)
            || !['open', 'resolved', 'superseded', 'expired'].includes(item.state)
            || typeof item.title !== 'string' || typeof item.safe_summary !== 'string'
            || !Number.isFinite(item.updated_at) || !item.target
            || !['task', 'inbox', 'approval'].includes(item.target.kind)) throw new Error('Invalid attention item')
          const previous = records.get(item.attention_id)
          if (!previous || item.revision > previous.revision) {
            records.set(item.attention_id, item)
            changesToNotify.set(item.attention_id, item)
          }
        }
        more = result.has_more
        if (more && result.cursor === cursor) throw new Error('Attention cursor did not advance')
        cursor = result.cursor
        if (!more) break
      }
      publish({ items: visible(), loading: more, available: true, error: '', updatedAt: clock(), syncRevision: snapshot.syncRevision + 1 })
      if (more) return // Finish the initial snapshot before producing one digest.
      const candidates = (baseline ? visible().filter(item => ACTIONABLE.has(item.kind)) : [...changesToNotify.values()]).filter(item => isOpen(item) && (ACTIONABLE.has(item.kind) || !isRead(item)))
      diagnose({ claimState: 'requesting', candidates: candidates.length, claimed: 0 })
      let fresh
      try {
        fresh = await claimAttempts(candidates, keyFor(), diagnose)
      } catch (error) {
        const stage = snapshot.diagnostics.claimState === 'locked' ? 'claim_storage'
          : snapshot.diagnostics.webLocks === 'available' ? 'web_lock' : 'claim'
        diagnose({ claimState: 'failed', lastErrorStage: stage, lastErrorName: errorName(error), lastErrorAt: clock() })
        return // Keep the current feed and candidates; retry the claim next poll.
      }
      if (disposed || turn !== generation || expectedScope !== scope()) return
      diagnose({ claimed: fresh.length, claimState: snapshot.diagnostics.claimState === 'unavailable' ? 'unavailable' : fresh.length ? 'claimed' : candidates.length ? 'already_claimed' : 'no_candidates' })
      baseline = false
      changesToNotify.clear()
      if (fresh.length) {
        // This is an attempt, not evidence that the OS displayed the notice.
        // Actual pending/resolved state remains in the runtime database.
        try { await notify(fresh.length, fresh[0], diagnose) }
        catch (error) { diagnose({ lastErrorStage: 'notification', lastErrorName: errorName(error), lastErrorAt: clock() }) }
      }
    } catch {
      if (!disposed && turn === generation && expectedScope === scope()) {
        publish({ loading: false, error: '暂时无法同步。待办仍保存在 agent 中；恢复连接后自动重试。' })
      }
    } finally {
      busy = false
    }
  }
  return {
    getSnapshot: () => snapshot,
    subscribe: listener => { listeners.add(listener); return () => listeners.delete(listener) },
    poll, reset, isOpen, isPending, isRead, isUnread,
    markRead(item) {
      const current = records.get(item.attention_id)
      if (!current || current.revision !== item.revision) return
      const key = `seen:${keyFor()}`
      persist(key, { ...(readPersist(key) || {}), [item.attention_id]: item.revision })
      publish({ items: visible() })
    },
    dispose: () => { disposed = true; generation += 1; listeners.clear() }
  }
}

export default {
  id: 'agent-comm-attention',
  name: '协作待办',
  description: '持久待办、纯提醒和 Hermes 原生对话恢复',
  defaultEnabled: false,
  register(ctx) {
    const scope = () => JSON.stringify([host.state.connectionId?.get() || '', host.state.profile.get()])
    const revealCenter = () => {
      // host.navigate writes the hash. Reopening the same route does not emit
      // a route change, so an intervening session tile needs an explicit reveal.
      if (typeof host.revealPane === 'function') host.revealPane('workspace')
    }
    const openCenter = () => { host.navigate(PAGE); revealCenter() }
    const controller = createAttentionController({
      rest: ctx.rest,
      scope,
      readPersist: key => ctx.storage.get(`attempts:${key}`, {}),
      persist: (key, value) => ctx.storage.set(`attempts:${key}`, value),
      async claim(items, key, diagnose) {
        const supported = typeof globalThis.navigator?.locks?.request === 'function'
        diagnose({ webLocks: supported ? 'available' : 'unavailable', secureContext: typeof globalThis.isSecureContext === 'boolean' ? globalThis.isSecureContext : null })
        if (!supported) { diagnose({ claimState: 'unavailable' }); return [] }
        return navigator.locks.request(`agent-comm-attention:${key}`, () => {
          diagnose({ claimState: 'locked' })
          const previous = ctx.storage.get(`attempts:${key}`, {})
          const fresh = items.filter(item => (previous[item.attention_id] || 0) < item.revision)
          for (const item of fresh) previous[item.attention_id] = item.revision
          ctx.storage.set(`attempts:${key}`, previous)
          return fresh
        })
      },
      async notify(count, _item, diagnose) {
        const title = '协作有新进展'
        const message = `有 ${count} 项协作进展。打开待办中心查看最新状态；需要决定时在原生对话中处理。`
        diagnose({ inAppCall: 'calling' })
        try {
          host.notify({ id: 'agent-comm-attention', kind: 'info', title, message,
            action: { label: '查看待办', onClick: openCenter } })
          diagnose({ inAppCall: 'returned' })
        } catch (error) {
          diagnose({ inAppCall: 'failed', lastErrorStage: 'in_app', lastErrorName: errorName(error), lastErrorAt: Date.now() })
          return
        }
        diagnose(previous => ({ osCall: 'calling', osAttempts: previous.osAttempts + 1, osAttemptAt: Date.now(),
          hidden: typeof globalThis.document?.hidden === 'boolean' ? document.hidden : null,
          focused: typeof globalThis.document?.hasFocus === 'function' ? document.hasFocus() : null }))
        try {
          // The real SDK returns void and may silently suppress a notice. Await
          // also handles a rejecting future SDK without claiming OS delivery.
          await ctx.os.notify({ title, body: message, activate: PAGE, onActivate: revealCenter })
          diagnose({ osCall: 'returned_unconfirmed' })
        } catch (error) {
          diagnose({ osCall: 'failed', lastErrorStage: 'os_call', lastErrorName: errorName(error), lastErrorAt: Date.now() })
        }
      }
    })
    const buttonStyle = { padding: '8px 12px', border: '1px solid var(--ui-border, #777)', borderRadius: 7, cursor: 'pointer' }
    async function resume(item) {
      const before = controller.getSnapshot()
      const expectedScope = scope()
      await controller.poll()
      const current = controller.getSnapshot()
      const latest = current.items.find(record => record.attention_id === item.attention_id)
      if (scope() !== expectedScope || before.owner !== current.owner || current.syncRevision === before.syncRevision || current.error || current.loading
        || !latest || latest.revision !== item.revision || !controller.isOpen(latest)) {
        host.notify({ kind: 'warning', message: '待办或连接已变化，请查看最新状态后再打开。' })
        return
      }
      const instruction = latest.resume?.instruction
      if (typeof instruction !== 'string') return
      const profile = host.state.profile.get()
      // Explicit user click copies a recovery request and navigates only.
      // It never submits that request, answers an approval, or starts a model.
      await ctx.os.writeClipboard(instruction)
      if (scope() !== expectedScope) return
      const session = latest.resume?.stored_session_id
      try {
        if (session) await host.openSession(session, { profile, awaitHydration: true, forceResume: true, hydrationTimeoutMs: 10000 })
        else host.newChat(profile)
      } catch {
        if (scope() === expectedScope) host.newChat(profile)
      }
      if (scope() === expectedScope) host.notify({ kind: 'info', message: '请粘贴恢复指令并发送；有效问题会在原生问题卡重新展示。此操作没有批准任何动作。' })
    }
    function Center() {
      const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot)
      const pending = state.items.filter(controller.isPending)
      const updates = state.items.filter(item => controller.isOpen(item) && !controller.isPending(item))
      const other = state.items.filter(item => !controller.isOpen(item))
      const unread = state.items.filter(controller.isUnread)
      const card = item => h('section', { key: item.attention_id, style: { border: '1px solid var(--ui-border, #777)', borderRadius: 10, padding: 18, marginTop: 16 } },
        h('h2', { style: { fontSize: 17, marginTop: 0 } }, item.title),
        h('p', { style: { whiteSpace: 'pre-wrap' } }, item.safe_summary),
        h('p', { style: { fontSize: 12, opacity: .72 } }, `${controller.isUnread(item) ? '未读' : '已读'}${item.task_id ? ` · 事项 ${item.task_id}` : ''}`),
        item.kind !== 'collaboration_completed' ? h('button', { style: buttonStyle, disabled: !!state.error || state.loading, onClick: () => void resume(item) }, '复制指令并打开原生对话') : null,
        controller.isUnread(item) ? h('button', { style: { ...buttonStyle, marginLeft: 8 }, onClick: () => controller.markRead(item) }, '标为已读') : null,
        item.kind !== 'collaboration_completed' ? h('details', { style: { marginTop: 10 } }, h('summary', null, '恢复指令'), h('pre', { style: { whiteSpace: 'pre-wrap', userSelect: 'text' } }, item.resume?.instruction || '请在原生对话中读取 agent_comm_collaboration state。')) : null)
      return h('main', { style: { padding: 28, maxWidth: 920, margin: '0 auto', overflow: 'auto', height: '100%' } },
        h('h1', null, '协作待办'),
        h('p', null, `未读 ${unread.length} · 待处理 ${pending.length} · 当前 profile：${host.state.profile.get() || '默认'}`),
        h('p', { style: { opacity: .72 } }, '通知只提醒你查看。授权需要在 Hermes 原生问题卡回答；已读、关闭或打开通知都不会批准。'),
        !globalThis.navigator?.locks ? h('p', null, '此客户端缺少跨窗口提醒去重能力；待办仍在这里，系统提醒暂不可用。') : null,
        state.error ? h('p', { role: 'alert' }, state.error) : null,
        h('button', { style: buttonStyle, onClick: () => controller.poll() }, state.loading ? '正在同步…' : '刷新'),
        state.updatedAt ? h('small', { style: { marginLeft: 12 } }, `最近同步 ${new Date(state.updatedAt).toLocaleTimeString()}`) : null,
        h('details', { style: { marginTop: 12 } }, h('summary', null, '通知诊断（不含内容）'),
          h('pre', { style: { whiteSpace: 'pre-wrap', fontSize: 12 } }, diagnosticText(state.diagnostics))),
        !pending.length && !state.loading ? h('p', null, state.available ? '当前没有待处理事项。' : '启用个人协作后，待办会显示在这里。') : null,
        pending.length ? h('h2', null, '待处理') : null,
        ...pending.map(card),
        updates.length ? h('h2', { style: { marginTop: 28 } }, '最新进展') : null,
        ...updates.map(card),
        other.length ? h('details', { style: { marginTop: 24 } }, h('summary', null, `已解决或失效 ${other.length} 项`),
          ...other.map(item => h('p', { key: item.attention_id }, `${item.title} · ${{ resolved: '已解决', superseded: '已被更新', expired: '已过期', open: '已过期' }[item.state]}`))) : null)
    }
    function Count() {
      const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot)
      const count = state.items.filter(controller.isPending).length
      const unread = state.items.filter(controller.isUnread).length
      return h('button', { title: `${state.error || '打开协作待办'}\n${diagnosticText(state.diagnostics)}`, onClick: openCenter, style: { border: 0, background: 'transparent', cursor: 'pointer' } }, `协作 · 未读 ${unread} · 待处理 ${count}${state.error ? ' · 离线' : ''}`)
    }
    ctx.registerMany([
      { id: 'page', area: ROUTES_AREA, data: { path: PAGE }, render: () => h(Center) },
      { id: 'nav', area: SIDEBAR_NAV_AREA, order: 55, data: { codicon: 'bell', label: '协作待办', path: PAGE } },
      { id: 'count', area: 'statusBar.right', order: 85, render: () => h(Count) }
    ])
    const onScopeChange = () => { controller.reset(); void controller.poll() }
    const disposers = [host.state.profile.listen(onScopeChange)]
    if (host.state.connectionId?.listen) disposers.push(host.state.connectionId.listen(onScopeChange))
    const timer = setInterval(() => void controller.poll(), 15000)
    ctx.onDispose(() => { clearInterval(timer); for (const dispose of disposers) dispose(); controller.dispose() })
    void controller.poll()
  }
}
