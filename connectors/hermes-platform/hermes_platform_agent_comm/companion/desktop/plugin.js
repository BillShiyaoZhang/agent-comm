import { host, ROUTES_AREA, SIDEBAR_NAV_AREA } from '@hermes/plugin-sdk'
import { createElement as h, useSyncExternalStore } from 'react'

const PAGE = '/agent-comm-attention'
const OPEN = new Set(['open'])
const ACTIONABLE = new Set(['owner_decision_required', 'new_collaboration_request', 'friend_request_received', 'needs_recovery', 'needs_response'])
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
export function createAttentionController({ rest, scope, notify, notifyOS, canNotifyOS = () => true, persist, readPersist, claim, clock = Date.now }) {
  let snapshot = { items: [], scope: scope(), owner: null, loading: true, error: '', available: true, updatedAt: null, syncRevision: 0, detailStates: {}, diagnostics: emptyDiagnostics() }
  let cursor = 0
  let generation = 0
  let busy = false
  let disposed = false
  let baseline = true
  const records = new Map()
  const changesToNotify = new Map()
  const systemCandidates = new Map()
  const detailFlights = new Map()
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
    systemCandidates.clear()
    detailFlights.clear()
    publish({ items: [], scope: scope(), owner: null, loading: true, error: '', available: true, updatedAt: null, detailStates: {}, diagnostics: emptyDiagnostics() })
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
          systemCandidates.clear()
          detailFlights.clear()
          cursor = 0
          baseline = true
          publish({ items: [], detailStates: {}, loading: false, available: false, error: '此 profile 尚未启用个人协作。' })
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
            || !['task', 'inbox', 'approval', 'contact'].includes(item.target.kind)) throw new Error('Invalid attention item')
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
      for (const item of candidates) systemCandidates.set(item.attention_id, item)
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
      if (notifyOS && !disposed && turn === generation && expectedScope === scope()) {
        const pending = [...systemCandidates.values()].filter(item => {
          const current = records.get(item.attention_id)
          return current?.revision === item.revision && isOpen(current) && !isRead(current)
            && (ACTIONABLE.has(current.kind) || clock() - current.updated_at * 1000 < 60000)
        })
        if (pending.length && !canNotifyOS()) diagnose({ osCall: 'deferred_foreground' })
        else if (pending.length) {
          // Foreground in-app delivery has a separate watermark. It must not
          // consume the one later OS attempt while the user is away.
          try {
            const systemFresh = await claimAttempts(pending, `os:${keyFor()}`, diagnose)
            if (disposed || turn !== generation || expectedScope !== scope()) return
            if (systemFresh.length) await notifyOS(systemFresh.length, systemFresh[0], diagnose)
          } catch (error) {
            diagnose({ lastErrorStage: 'os_claim', lastErrorName: errorName(error), lastErrorAt: clock() })
          }
        }
      }
    } catch {
      if (!disposed && turn === generation && expectedScope === scope()) {
        publish({ loading: false, error: '暂时无法同步。待办仍保存在 agent 中；恢复连接后自动重试。' })
      }
    } finally {
      busy = false
    }
  }
  function loadDetail(item) {
    if (disposed || !snapshot.available || scope() !== snapshot.scope || !snapshot.owner || !records.has(item.attention_id)) return Promise.resolve()
    if (detailFlights.has(item.attention_id)) return detailFlights.get(item.attention_id)
    const turn = generation, expectedScope = scope(), expectedOwner = snapshot.owner
    const current = () => !disposed && snapshot.available && turn === generation && expectedScope === scope() && expectedOwner === snapshot.owner
    const status = changes => { if (current()) publish({ detailStates: { ...snapshot.detailStates, [item.attention_id]: changes } }) }
    status({ loading: true, error: '', updatedAt: snapshot.detailStates[item.attention_id]?.updatedAt })
    const run = Promise.resolve().then(async () => {
      try {
        if (!current()) return
        const result = await rest(`/attention/${encodeURIComponent(item.attention_id)}/detail`, { timeoutMs: 10000 })
        if (!current()) return
        const detail = result?.item
        if (result?.owner_key !== expectedOwner || result.available !== true || detail?.attention_id !== item.attention_id
          || !Number.isSafeInteger(detail.revision) || !['open', 'resolved', 'superseded', 'expired'].includes(detail.state)
          || typeof detail.title !== 'string' || typeof detail.safe_summary !== 'string' || !Number.isFinite(detail.updated_at)
          || !detail.target || !['task', 'inbox', 'approval', 'contact'].includes(detail.target.kind)
          || !detail.details || typeof detail.details !== 'object') throw new Error('Unavailable detail')
        const previous = records.get(item.attention_id)
        if (previous && detail.revision >= previous.revision) {
          records.set(item.attention_id, detail)
          // Reading context does not consume the feed cursor or display a notice.
          // A new source version still flows through the normal next poll.
          if (detail.revision > previous.revision) changesToNotify.set(item.attention_id, detail)
          publish({ items: visible() })
        }
        status({ loading: false, error: '', updatedAt: clock() })
      } catch {
        status({ loading: false, error: '暂时无法读取最新背景。下面保留的是上次同步的内容，请重试后再作决定。', updatedAt: snapshot.detailStates[item.attention_id]?.updatedAt })
      } finally { if (current()) detailFlights.delete(item.attention_id) }
    })
    detailFlights.set(item.attention_id, run)
    return run
  }
  return {
    getSnapshot: () => snapshot,
    subscribe: listener => { listeners.add(listener); return () => listeners.delete(listener) },
    poll, reset, loadDetail, isOpen, isPending, isRead, isUnread,
    async markRead(item) {
      const current = records.get(item.attention_id)
      if (!current || current.revision !== item.revision) return
      const turn = generation, expectedScope = scope()
      if (current.target.kind === 'inbox') {
        try {
          await rest('/attention/mark-read', { method: 'POST', body: { message_id: current.target.id }, timeoutMs: 10000 })
        } catch {
          if (!disposed && turn === generation && expectedScope === scope()) publish({ error: '未能保存已读状态，请重试；其它客户端的提醒仍保留。' })
          return
        }
        if (disposed || turn !== generation || expectedScope !== scope()) return
      }
      const key = `seen:${keyFor()}`
      persist(key, { ...(readPersist(key) || {}), [item.attention_id]: item.revision })
      publish({ items: visible() })
      if (current.target.kind === 'inbox') await poll()
    },
    dispose: () => { disposed = true; generation += 1; detailFlights.clear(); listeners.clear() }
  }
}

// Processing is a separate, explicit-click path. Polling and notification
// activation never create a chat, submit a turn, answer a question or dispatch.
export function handlingSessionTitle(title, storedSessionId) {
  // Hermes titles are unique per profile and capped at 100 characters. Use the
  // actual persisted-session identity, not the generic attention title or the
  // shared resume ID: two devices may create different drafts before bind CAS.
  const prefix = '协作处理 · ', suffix = ` · ${storedSessionId}`
  const available = 100 - prefix.length - suffix.length
  if (!/^[A-Za-z0-9_.:-]+$/.test(storedSessionId) || available < 1) throw new Error('Unsupported native session identifier')
  const label = String(title || '协作待办').replace(/[\u0000-\u001f\u007f]/g, ' ').trim() || '协作待办'
  return prefix + label.slice(0, available) + suffix
}

export function createResumeController({ rest, host, scope, readAttention, lock, persist = () => {}, readPersist = () => null, clock = Date.now }) {
  let snapshot = { busy: false, entries: {} }, generation = 0, disposed = false
  const listeners = new Set(), flights = new Map()
  const publish = changes => { snapshot = { ...snapshot, ...changes }; for (const listener of listeners) listener() }
  const failure = phase => Object.assign(new Error(phase), { resumePhase: phase })
  const validId = value => typeof value === 'string' && /^[A-Za-z0-9_.:-]{1,240}$/.test(value)
  // Electron invoke preserves the HTTP error message but may drop custom
  // statusCode fields. Read only the transport's fixed numeric prefix.
  const httpStatus = error => error?.statusCode ?? error?.status ?? Number(/^(?:Error invoking remote method ['"]hermes:api['"]: )?(?:Error: )?([1-5]\d\d):/.exec(error?.message || '')?.[1])
  const accepted = new Set(['streaming', 'queued', 'steered', 'redirected'])
  const messages = {
    checking: '正在核对最新背景…', creating: '正在创建关联处理会话…', opening: '正在打开关联会话…',
    submitting: '正在把背景带入 Hermes…', submitted: '背景已带入 Hermes。需要你决定时，会在该会话显示原生问题卡。',
    queued: '背景已排队，当前回合结束后继续处理。', reopened: '已打开关联处理会话；没有重复发送背景。',
    uncertain: '背景是否已送达尚待核对。已保留关联会话，不会自动重复发送；请查看会话中的当前状态。',
    changed: '待办或连接已变化，请刷新后查看最新状态。', expired: '这项待办已结束或过期，无需继续处理。',
    unsupported: '当前 Hermes 缺少安全打开处理会话所需的接口，请更新客户端。',
    failed: '暂时无法打开处理流程。待办仍保留，请检查连接后重试。'
  }
  const failureMessages = {
    connecting: '连接处理会话服务失败。待办仍保留，请检查 Hermes 连接后重试。',
    preparing: '读取最新处理背景失败。待办仍保留，请刷新后重试。',
    creating: '创建处理会话失败。待办仍保留，请检查 Hermes 连接后重试。',
    recovering_draft: '恢复已有处理草稿失败。草稿已保留；不会自动另建会话，请检查连接后重试。',
    titling: '保存处理会话名称失败。草稿记录已保留，重试会先尝试恢复。',
    binding: '关联处理会话失败。现有草稿已保留，重试会继续核对关联。',
    opening: '打开关联处理会话失败。会话已保留；本次没有重复发送处理背景，请检查连接后重试。',
    verifying_session: '核对关联处理会话失败。未发送处理背景，请检查连接后重试。',
    claiming: '核对背景提交状态失败。没有重复发送背景，请刷新后重试。'
  }
  function reset() { generation++; flights.clear(); publish({ busy: false, entries: {} }) }
  function start(item) {
    if (flights.has(item.attention_id)) return flights.get(item.attention_id)
    if (snapshot.busy || disposed) return Promise.resolve()
    const turn = generation, expectedScope = scope(), expectedOwner = readAttention().owner
    const current = () => !disposed && turn === generation && expectedScope === scope() && expectedOwner === readAttention().owner
    const check = () => { if (!current()) throw failure('changed') }
    const update = (phase, extra = {}) => {
      if (current()) publish({ entries: { ...snapshot.entries, [item.attention_id]: { ...snapshot.entries[item.attention_id], revision: item.revision, phase, message: messages[phase], failureStage: null, ...extra } } })
    }
    const post = async (path, body) => { check(); const result = await rest(path, { method: 'POST', body, timeoutMs: 15000 }); check(); return result }
    publish({ busy: true }); update('checking')
    const run = Promise.resolve().then(async () => {
      let release, stage = 'connecting'
      try {
        if (!OPEN.has(item.state) || (item.expires_at && item.expires_at * 1000 <= clock()) || item.details?.can_resume === false) throw failure('expired')
        if (host.state.gateway?.get && host.state.gateway.get() !== 'open') throw failure('failed')
        if (!expectedOwner || typeof lock !== 'function' || typeof host.profileRoutes !== 'function'
          || typeof host.requestProfile !== 'function' || typeof host.retainProfile !== 'function' || typeof host.openSession !== 'function') throw failure('unsupported')
        const profile = host.state.profile.get() || 'default', connectionId = host.state.connectionId?.get()
        const routes = await host.profileRoutes(); check()
        const matching = Array.isArray(routes) ? routes.filter(route => route.profile === profile && (connectionId ? route.connectionId === connectionId : route.mode === 'local')) : []
        if (matching.length !== 1 || !matching[0].targetProfile) throw failure('unsupported')
        const route = matching[0]
        release = await host.retainProfile(route); check()
        const request = async (method, params) => { check(); const result = await host.requestProfile(route, method, { ...params, profile: route.targetProfile }, 20000); check(); return result }
        await lock(`agent-comm-resume:${JSON.stringify([expectedScope, item.task_id || item.attention_id])}`, async () => {
          check()
          stage = 'preparing'
          const prepared = await post('/attention/prepare-resume', { attention_id: item.attention_id, revision: item.revision })
          if (prepared.schema !== 'agent-comm-resume/v1' || prepared.owner_key !== expectedOwner || !validId(prepared.resume_id)
            || prepared.item?.attention_id !== item.attention_id || prepared.item?.revision !== item.revision
            || !['available', 'missing', 'none'].includes(prepared.session_state)
            || !['ready', 'submitting', 'submitted', 'uncertain'].includes(prepared.submission_state)
            || typeof prepared.instruction !== 'string' || !prepared.instruction || prepared.instruction.length > 65536) throw failure('changed')
          if (!OPEN.has(prepared.item.state) || prepared.item.details?.can_resume === false
            || (prepared.item.expires_at && prepared.item.expires_at * 1000 <= clock())) throw failure('expired')
          update('checking', { details: prepared.item.details })
          let stored = prepared.stored_session_id
          if (prepared.session_state === 'available' && !validId(stored)) throw failure('changed')
          if (prepared.session_state !== 'available') {
            if (prepared.submission_state !== 'ready') throw failure('changed')
            update('creating')
            const draftKey = `resume-draft:${JSON.stringify([expectedScope, prepared.resume_id])}`
            let created = readPersist(draftKey)
            if (validId(created?.stored_session_id)) {
              stage = 'recovering_draft'
              try { created = { ...created, ...(await request('session.resume', { session_id: created.stored_session_id, source: 'desktop', omit_messages: true })) } }
              catch (error) {
                if (error?.code !== 4007) throw error // Real session.resume: exact stored session not found.
                created = null
              }
            } else created = null
            if (!created) {
              stage = 'creating'
              // New chats are lazy. Assign their final unique title only after
              // the host returns the real stored ID; no generic title can race
              // another draft into the profile's unique title namespace.
              created = await request('session.create', { source: 'desktop', close_on_disconnect: false })
              if (!validId(created?.stored_session_id) || !validId(created?.session_id)) throw failure('changed')
              persist(draftKey, { stored_session_id: created.stored_session_id })
            }
            if (!validId(created.session_id) || !validId(created.stored_session_id)) throw failure('changed')
            // A new session is lazy. Persist its titled row before binding or
            // opening, so later clicks can recover it without duplicate drafts.
            stage = 'titling'
            await request('session.title', { session_id: created.session_id, title: handlingSessionTitle(prepared.item.title, created.stored_session_id) })
            stage = 'binding'
            const bound = await post('/attention/bind-session', { resume_id: prepared.resume_id, stored_session_id: created.stored_session_id })
            if (!validId(bound.stored_session_id)) throw failure('changed')
            stored = bound.stored_session_id // A concurrent window's binding wins.
            persist(draftKey, null)
          }
          stage = 'opening'
          update('opening', { storedSessionId: stored })
          await host.openSession(stored, { route, awaitHydration: true, forceResume: true, hydrationTimeoutMs: 15000 }); check()
          if (prepared.submission_state !== 'ready') {
            update(prepared.submission_state === 'submitted' ? 'reopened' : 'uncertain', { storedSessionId: stored })
            return
          }
          stage = 'verifying_session'
          const live = await request('session.resume', { session_id: stored, source: 'desktop', omit_messages: true })
          if (!validId(live?.session_id)) throw failure('changed')
          stage = 'claiming'
          const claimed = await post('/attention/claim-submit', { resume_id: prepared.resume_id, stored_session_id: stored })
          if (claimed.claimed !== true) {
            update(claimed.submission_state === 'submitted' ? 'reopened' : 'uncertain', { storedSessionId: stored })
            return
          }
          if (!validId(claimed.claim_token)) throw failure('changed')
          update('submitting', { storedSessionId: stored })
          let outcome = 'uncertain', result
          try {
            result = await request('prompt.submit', { session_id: live.session_id, text: prepared.instruction, queued: true })
            if (accepted.has(result?.status)) outcome = 'submitted'
          } catch { /* A lost response cannot justify another submission. */ }
          if (!current()) return
          try { await post('/attention/finish-submit', { resume_id: prepared.resume_id, claim_token: claimed.claim_token, outcome }) }
          catch { update('uncertain', { storedSessionId: stored }); return }
          update(outcome === 'submitted' ? result.status === 'queued' ? 'queued' : 'submitted' : 'uncertain', { storedSessionId: stored })
        })
      } catch (error) {
        const phase = error?.resumePhase || (httpStatus(error) === 409 ? 'changed' : 'failed')
        update(phase, { failureStage: stage, ...(phase === 'failed' ? { message: failureMessages[stage] || messages.failed } : {}) })
      } finally {
        try { if (typeof release === 'function') release() } catch { /* Retention cleanup cannot trigger a duplicate submission. */ }
        if (current()) { flights.delete(item.attention_id); publish({ busy: false }) }
      }
    })
    flights.set(item.attention_id, run)
    return run
  }
  return { start, reset, getSnapshot: () => snapshot, subscribe: listener => { listeners.add(listener); return () => listeners.delete(listener) },
    dispose: () => { disposed = true; generation++; listeners.clear(); flights.clear() } }
}

export default {
  id: 'agent-comm-attention',
  name: '协作待办',
  description: '持久待办、纯提醒和 Hermes 原生对话恢复',
  defaultEnabled: true,
  register(ctx) {
    const scope = () => JSON.stringify([host.state.connectionId?.get() || '', host.state.profile.get()])
    const systemAllowed = () => Boolean(globalThis.document && (document.hidden || (typeof document.hasFocus === 'function' && !document.hasFocus())))
    const revealCenter = () => {
      // host.navigate writes the hash. Reopening the same route does not emit
      // a route change, so an intervening session tile needs an explicit reveal.
      if (typeof host.revealPane === 'function') host.revealPane('workspace')
    }
    const openCenter = () => { host.navigate(PAGE); revealCenter() }
    const controller = createAttentionController({
      rest: ctx.rest,
      scope,
      canNotifyOS: systemAllowed,
      readPersist: key => ctx.storage.get(`attempts:${key}`, {}),
      persist: (key, value) => ctx.storage.set(`attempts:${key}`, value),
      async claim(items, key, diagnose) {
        const supported = typeof globalThis.navigator?.locks?.request === 'function'
        diagnose({ webLocks: supported ? 'available' : 'unavailable', secureContext: typeof globalThis.isSecureContext === 'boolean' ? globalThis.isSecureContext : null })
        if (!supported) { diagnose({ claimState: 'unavailable' }); return [] }
        return navigator.locks.request(`agent-comm-attention:${key}`, () => {
          if (key.startsWith('os:') && !systemAllowed()) { diagnose({ osCall: 'deferred_foreground' }); return [] }
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
        }
      },
      async notifyOS(count, _item, diagnose) {
        const title = '协作有新进展'
        const message = `有 ${count} 项协作进展。打开待办中心查看最新状态；需要决定时在原生对话中处理。`
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
    const resumer = createResumeController({ rest: ctx.rest, host, scope, readAttention: controller.getSnapshot,
      lock: typeof globalThis.navigator?.locks?.request === 'function' ? (key, callback) => navigator.locks.request(key, callback) : undefined,
      persist: (key, value) => ctx.storage.set(key, value), readPersist: key => ctx.storage.get(key, null) })
    const buttonStyle = { padding: '8px 12px', border: '1px solid var(--ui-border, #777)', borderRadius: 7, cursor: 'pointer' }
    const textStyle = { whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }
    const time = value => {
      const date = Number.isFinite(value) ? new Date(value * 1000) : typeof value === 'string' && value ? new Date(value) : null
      return date && Number.isFinite(date.getTime()) ? date.toLocaleString() : '未设定'
    }
    const labels = { purpose: '目的', topic: '主题', participants: '参与人', recipients: '信息接收方',
      participant_ids: '参与人', recipient_ids: '信息接收方', capabilities: '允许动作',
      allowed_actions: '允许动作', allowed_fields: '允许披露的信息', protected_fields: '保护的信息',
      expires_at: '截止时间', deadline: '截止时间', duration_minutes: '时长（分钟）', timezone: '时区',
      time_window: '时间范围', window_start: '最早时间', window_end: '最晚时间', allowed_windows: '可接受时间段',
      budget: '预算', max_rounds: '最多协商轮次', max_candidates: '最多候选时间数', max_actions: '累计动作上限',
      max_duration_minutes: '最长时长（分钟）', min_duration_minutes: '最短时长（分钟）',
      start_at: '开始时间', end_at: '结束时间', start: '开始时间', end: '结束时间', location: '地点', medium: '形式', notes: '备注',
      resource_ids: '允许分享的资料', text: '内容', action: '动作', kind: '类型', status: '状态', terms: '方案', payload: '具体内容',
      phase: '协作阶段', waiting_reason: '等待原因', agreement: '双方方案', initiator_urn: '发起方 Agent', peer_urn: '对端 Agent',
      proposal_id: '方案编号', version: '方案版本', revision: '修订版本', cancel_request: '取消请求' }
    const actionNames = { share_slots: '分享候选空档', share_resource: '分享指定资料全文', propose_meeting: '提出会议方案',
      accept_meeting: '接受会议方案', send_text: '发送自由文本（每次须确认确切全文）' }
    const facts = (value, depth = 0) => {
      if (value == null) return '未提供'
      if (typeof value === 'boolean') return value ? '允许' : '不允许'
      if (typeof value !== 'object') return String(value)
      if (depth > 5) return '请在处理会话中查看完整内容'
      if (Array.isArray(value)) return h('ul', null, ...value.map((entry, index) => h('li', { key: index }, facts(entry, depth + 1))))
      return h('dl', null, ...Object.entries(value).filter(([key]) => !['task_id', 'operation_id', 'message_id', 'worker'].includes(key))
        .flatMap(([key, entry]) => [h('dt', { key: `${key}:label`, style: { fontWeight: 600, marginTop: 8 } }, labels[key] || key),
          h('dd', { key, style: { marginLeft: 16, ...textStyle } }, key === 'expires_at' ? time(entry)
            : key === 'capabilities' && Array.isArray(entry) ? facts(entry.map(action => actionNames[action] || action), depth + 1)
              : facts(entry, depth + 1))]))
    }
    const workerView = worker => worker ? h('section', { style: { marginTop: 16 } },
      h('h3', null, '自动协作范围'),
      h('p', null, `当前状态：${({ disabled: '未启用', pending: '等待你的授权', active: '已启用', paused: '已暂停', revoked: '已撤销', exhausted: '预算已用完', expired: '已到期' })[worker.status] || '正在核对'}`),
      worker.policy ? h('ul', null,
        h('li', null, `提出方案：${worker.policy.allow_propose ? '允许' : '不允许'}；接受方案：${worker.policy.allow_accept ? '允许' : '不允许'}`),
        h('li', null, `运行次数：${worker.runs_used ?? 0} / ${worker.policy.max_runs ?? 0}；发送次数：${worker.sends_used ?? 0} / ${worker.policy.max_sends ?? 0}`),
        h('li', null, `检查间隔：${worker.policy.interval_seconds ?? '未提供'} 秒；仅限已授权的这一项协作。`),
        h('li', null, `有效期至：${time(worker.policy.expires_at)}`)) : null,
      worker.policy?.proposal ? h('details', null, h('summary', null, '允许发送的固定方案'), facts(worker.policy.proposal)) : null,
      worker.waiting_reason ? h('p', { style: textStyle }, `暂停或等待原因：${worker.waiting_reason}`) : null,
      h('p', { style: { opacity: .72 } }, '启用、调整或撤销自动协作范围，请进入处理会话说明；启用前需要回答包含具体预算的原生授权问题。')) : null
    const detailView = (item, details, detailState) => details ? h('details', { style: { marginTop: 12 },
      onToggle: event => { if (event.target === event.currentTarget && event.currentTarget.open) void controller.loadDetail(item) } },
      h('summary', null, '查看完整背景与授权范围'),
      detailState?.loading ? h('p', { role: 'status' }, '正在核对最新背景…') : null,
      detailState?.error ? h('p', { role: 'alert', style: textStyle }, detailState.error) : null,
      detailState?.updatedAt ? h('p', { style: { fontSize: 12, opacity: .72 } }, `详情更新于 ${new Date(detailState.updatedAt).toLocaleTimeString()}`) : null,
      h('button', { style: buttonStyle, disabled: detailState?.loading === true, onClick: () => void controller.loadDetail(item) }, detailState?.error ? '重试读取背景' : '刷新背景'),
      details.initiator ? h('p', { style: textStyle }, `发起方：${details.initiator.label}${details.initiator.urn ? `（${details.initiator.urn}）` : ''}`) : null,
      details.question ? h('section', null, h('h3', null, '需要你决定的问题'), h('div', { style: textStyle }, facts(details.question))) : null,
      details.task?.scope ? h('section', null, h('h3', null, '当前委托范围'), facts(details.task.scope)) : null,
      details.operation ? h('section', null, h('h3', null, '待处理动作'), facts(details.operation)) : null,
      details.contact ? h('section', null, h('h3', null, '联系人信息'), facts(details.contact)) : null,
      details.contact_request ? h('section', null, h('h3', null, '好友请求'), facts(details.contact_request)) : null,
      details.collaboration ? h('section', null, h('h3', null, '当前协作方案'), facts(details.collaboration)) : null,
      details.peer_message ? h('section', null, h('h3', null, '对端来信（待核实内容）'),
        h('blockquote', { style: textStyle }, details.peer_message.text),
        details.peer_message.truncated ? h('p', null, '较长来信已截短；处理会话会读取当前完整记录。') : null) : null,
      workerView(details.task?.worker),
      Array.isArray(details.risks) && details.risks.length ? h('section', null, h('h3', null, '决定前请留意'),
        h('ul', null, ...details.risks.map((risk, index) => h('li', { key: index, style: textStyle }, risk)))) : null) : null
    function Center() {
      const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot)
      const handling = useSyncExternalStore(resumer.subscribe, resumer.getSnapshot)
      const pending = state.items.filter(controller.isPending)
      const updates = state.items.filter(item => controller.isOpen(item) && !controller.isPending(item))
      const other = state.items.filter(item => !controller.isOpen(item))
      const unread = state.items.filter(controller.isUnread)
      const card = item => {
        const entry = handling.entries[item.attention_id]
        const progress = entry?.revision === item.revision ? entry : null
        const details = item.details || progress?.details
        const canResume = controller.isOpen(item) && details?.can_resume !== false && item.kind !== 'collaboration_completed'
        return h('section', { key: item.attention_id, style: { border: '1px solid var(--ui-border, #777)', borderRadius: 10, padding: 18, marginTop: 16 } },
        h('h2', { style: { fontSize: 17, marginTop: 0 } }, item.title),
        h('p', { style: textStyle }, details?.context_summary || item.safe_summary),
        h('p', { style: { fontSize: 12, opacity: .72 } }, `${controller.isUnread(item) ? '未读' : '已读'}${item.expires_at ? ` · 有效期至 ${time(item.expires_at)}` : ''}`),
        canResume ? h('button', { style: buttonStyle, disabled: !!state.error || state.loading || handling.busy,
          onClick: () => void resumer.start(item) }, progress?.storedSessionId ? '打开处理会话' : '在 Hermes 中处理') : null,
        controller.isUnread(item) ? h('button', { style: { ...buttonStyle, marginLeft: 8 }, onClick: () => controller.markRead(item) }, '标为已读') : null,
        canResume ? h('p', { style: { fontSize: 13, opacity: .72 } }, item.resume?.session_state === 'available'
          ? '将打开关联会话并自动带入背景。'
          : '将恢复可用会话；原会话不存在时创建关联处理会话，并自动带入背景。') : null,
        progress ? h('p', { role: 'status', style: textStyle }, progress.message) : null,
        detailView(item, details, state.detailStates[item.attention_id]))
      }
      return h('main', { style: { padding: 28, maxWidth: 920, margin: '0 auto', overflow: 'auto', height: '100%' } },
        h('h1', null, '协作待办'),
        h('p', null, `未读 ${unread.length} · 待处理 ${pending.length} · 当前 profile：${host.state.profile.get() || '默认'}`),
        h('p', { style: { opacity: .72 } }, '查看背景后，点击“在 Hermes 中处理”即可带入背景并继续对话。授权需要在原生问题卡回答；打开待办或进入会话都不会批准。'),
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
          ...other.map(item => h('div', { key: item.attention_id },
            h('p', null, `${item.title} · ${{ resolved: '已解决', superseded: '已被更新', expired: '已过期', open: '已过期' }[item.state]}`), detailView(item, item.details, state.detailStates[item.attention_id])))) : null)
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
    const onScopeChange = () => { resumer.reset(); controller.reset(); void controller.poll() }
    const disposers = [host.state.profile.listen(onScopeChange)]
    let lastOwner = controller.getSnapshot().owner
    disposers.push(controller.subscribe(() => {
      const owner = controller.getSnapshot().owner
      if (owner !== lastOwner) { lastOwner = owner; resumer.reset() }
    }))
    if (host.state.connectionId?.listen) disposers.push(host.state.connectionId.listen(onScopeChange))
    const onAttentionChange = () => void controller.poll()
    if (globalThis.document?.addEventListener) {
      document.addEventListener('visibilitychange', onAttentionChange)
      disposers.push(() => document.removeEventListener('visibilitychange', onAttentionChange))
    }
    if (globalThis.addEventListener) {
      globalThis.addEventListener('blur', onAttentionChange)
      disposers.push(() => globalThis.removeEventListener('blur', onAttentionChange))
    }
    const timer = setInterval(() => void controller.poll(), 15000)
    ctx.onDispose(() => { clearInterval(timer); for (const dispose of disposers) dispose(); resumer.dispose(); controller.dispose() })
    void controller.poll()
  }
}
