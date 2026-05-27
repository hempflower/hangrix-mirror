<script setup lang="ts">
// AgentSessionsView renders the agents-tab content on the issue detail
// page. Two-pane layout:
//
//   ┌─── left ─────┬──── right ─────────────────────────────┐
//   │ session list │ selected session: identity + messages  │
//   └──────────────┴────────────────────────────────────────┘
//
// Polls the server every 5 s while the tab is active AND at least one
// session is still in a live state. Hidden tab (visibilitychange) and
// inactive tab (`active` prop false) both pause the poller.

import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import {
  AlertTriangle,
  ArrowDownToLine,
  Bot,
  ChevronRight,
  Cog,
  FileEdit,
  FilePlus,
  FileText,
  FolderSearch,
  Globe,
  Hammer,
  Keyboard,
  Megaphone,
  Play,
  RotateCcw,
  Search,
  Sparkles,
  Square,
  Terminal,
  Trash2,
  XCircle,
} from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

interface Props {
  active: boolean
  owner: string
  name: string
  issueNumber: number
}
const props = defineProps<Props>()
const { t } = useI18n()

interface AgentSession {
  session_id: number
  runner_id?: number
  role_key: string
  status: string
  repo_sha: string
  cause_kind: string
  cause_id: string
  role_config: {
    triggers?: string[]
    permission?: string
    tool_patterns?: string[]
    model?: string
    host_addendum?: string
    container?: { image?: string }
  }
  exit_code?: number
  error_message?: string
  created_at: string
  ended_at?: string | null
}

interface AgentMessage {
  id: number
  seq: number
  kind: string
  role?: string
  content?: string
  event?: string
  tool_call_id?: string
  tool_name?: string
  payload: unknown
  created_at: string
}

const sessions = ref<AgentSession[]>([])
const selectedId = ref<number | null>(null)
const messages = ref<AgentMessage[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

const baseUrl = computed(
  () => `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/agent-sessions`,
)

const selected = computed<AgentSession | null>(() => {
  if (selectedId.value == null) return null
  return sessions.value.find((s) => s.session_id === selectedId.value) ?? null
})

// "Live" = a runner might still emit messages on this session. Used both
// for the poll trigger and the row-level pulse indicator.
const liveStatuses = new Set(['pending', 'claimed', 'running', 'idle'])
function isLive(status: string) {
  return liveStatuses.has(status)
}
const hasLive = computed(() => sessions.value.some((s) => isLive(s.status)))

async function loadSessions() {
  try {
    const data = await $fetch<{ items: AgentSession[] }>(baseUrl.value)
    sessions.value = data.items ?? []
    error.value = null
    if (selectedId.value == null && sessions.value.length > 0) {
      // Default selection: first running session, falling back to the
      // most recently created one.
      const live = sessions.value.find((s) => isLive(s.status))
      selectedId.value = live ? live.session_id : sessions.value[sessions.value.length - 1]!.session_id
    }
  } catch (e: unknown) {
    const msg = (e as { data?: { error?: string } })?.data?.error ?? t('agentSessions.loadFailed')
    error.value = String(msg)
  }
}

async function loadMessages(sid: number) {
  try {
    const data = await $fetch<{ items: AgentMessage[] }>(`${baseUrl.value}/${sid}/messages`)
    // Only apply the response if the user hasn't switched to a different
    // session while the request was in flight — avoids flashing stale
    // messages from the previous selection.
    if (selectedId.value === sid) {
      messages.value = data.items ?? []
    }
  } catch {
    // Non-fatal: leave the existing log onscreen. The sessions list
    // error banner already surfaces backend trouble.
  }
}

async function refresh() {
  if (!props.active) return
  loading.value = sessions.value.length === 0
  await loadSessions()
  if (selectedId.value != null) await loadMessages(selectedId.value)
  loading.value = false
}

let timer: ReturnType<typeof setInterval> | null = null
function startPoll() {
  if (timer != null) return
  timer = setInterval(() => {
    if (document.hidden) return
    if (!props.active) return
    if (!hasLive.value) return
    void refresh()
  }, 5000)
}
function stopPoll() {
  if (timer != null) {
    clearInterval(timer)
    timer = null
  }
}

// React to tab visibility. Only kick off the initial fetch when the tab
// becomes active so we don't waste a request on hidden tabs.
watch(
  () => props.active,
  (a) => {
    if (a) {
      void refresh()
      startPoll()
    }
  },
  { immediate: true },
)
watch(selectedId, (sid) => {
  if (sid != null) void loadMessages(sid)
})
onUnmounted(stopPoll)

// ---- rendering helpers ----

function shortSha(s: string) {
  return s ? s.slice(0, 8) : ''
}

function duration(s: AgentSession) {
  const start = Date.parse(s.created_at)
  const end = s.ended_at ? Date.parse(s.ended_at) : Date.now()
  if (!start || !end) return ''
  return `${Math.max(0, Math.floor((end - start) / 1000))}s`
}

function offsetFrom(start: string, when: string) {
  const a = Date.parse(start)
  const b = Date.parse(when)
  if (!a || !b) return ''
  const sec = Math.max(0, Math.floor((b - a) / 1000))
  return `+${sec}s`
}

function causeLabel(s: AgentSession) {
  switch (s.cause_kind) {
    case 'issue_opened':
      return t('agentSessions.cause.issueOpened')
    case 'comment_mentioned':
      return t('agentSessions.cause.commentMentioned', { id: s.cause_id || '?' })
    case 'commit_pushed':
      return t('agentSessions.cause.commitPushed', { sha: shortSha(s.cause_id) })
    case 'review_vote':
      return t('agentSessions.cause.reviewVote', { id: s.cause_id || '?' })
    default:
      return s.cause_kind
  }
}

function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  if (status === 'running' || status === 'claimed' || status === 'pending') return 'default'
  if (status === 'failed' || status === 'cancelled') return 'destructive'
  return 'secondary'
}

const expanded = ref<Set<number>>(new Set())
function toggleExpand(seq: number) {
  if (expanded.value.has(seq)) {
    expanded.value.delete(seq)
  } else {
    expanded.value.add(seq)
  }
  // Trigger reactivity.
  expanded.value = new Set(expanded.value)
}
function isExpanded(seq: number) {
  return expanded.value.has(seq)
}

function payloadString(m: AgentMessage): string {
  if (m.payload == null) return ''
  try {
    return JSON.stringify(m.payload, null, 2)
  } catch {
    return String(m.payload)
  }
}

// ---- tool-call helpers ----
//
// The agent's JSONL tool_call frames bundle `args` (the tool's input) and
// `result` (the tool's output) into one payload. The collapsed-row chip
// and the tool-specific expanded view both read from the same envelope,
// so the helpers below normalise the access pattern instead of letting
// every renderer do its own `payload?.args?.foo` dance.

type AnyRecord = Record<string, unknown>

function asRecord(v: unknown): AnyRecord {
  return (v && typeof v === 'object' && !Array.isArray(v)) ? (v as AnyRecord) : {}
}

function payloadArgs(m: AgentMessage): AnyRecord {
  return asRecord(asRecord(m.payload).args)
}

function payloadResult(m: AgentMessage): AnyRecord {
  return asRecord(asRecord(m.payload).result)
}

function asString(v: unknown): string {
  if (v == null) return ''
  if (typeof v === 'string') return v
  return String(v)
}

function asNumber(v: unknown): number | null {
  if (typeof v === 'number' && Number.isFinite(v)) return v
  if (typeof v === 'string' && v !== '' && Number.isFinite(Number(v))) return Number(v)
  return null
}

function countLines(s: string): number {
  if (!s) return 0
  return s.split('\n').length
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}

// shortenPath drops everything but the trailing two segments of a path
// so the collapsed-row chip stays under one line. Absolute paths like
// /workspace/apps/foo/bar/baz.go become …/bar/baz.go; short paths pass
// through unchanged.
function shortenPath(p: string): string {
  if (!p) return ''
  const parts = p.split('/').filter(Boolean)
  if (parts.length <= 2) return p
  return '…/' + parts.slice(-2).join('/')
}

function shortenUrl(u: string): string {
  if (!u) return ''
  try {
    const x = new URL(u)
    let path = x.pathname
    if (path.length > 32) path = path.slice(0, 31) + '…'
    return x.host + path
  } catch {
    return truncate(u, 48)
  }
}

function toolIcon(name: string | undefined) {
  switch (name) {
    case 'read': return FileText
    case 'write': return FilePlus
    case 'edit': return FileEdit
    case 'bash': return Terminal
    case 'bash_input': return Keyboard
    case 'grep': return Search
    case 'glob': return FolderSearch
    case 'webfetch': return Globe
    default: return Hammer
  }
}

// toolSummary returns the inline label that sits next to the tool name
// in a collapsed tool_call row — "src/foo.go:1-100" for a read,
// "src/foo.go · -2 +5" for an edit, the LLM-supplied bash summary, etc.
// Returns '' when there's nothing meaningful to show (the row then just
// renders the tool name on its own).
function toolSummary(m: AgentMessage): string {
  const a = payloadArgs(m)
  const r = payloadResult(m)
  switch (m.tool_name) {
    case 'read': {
      const path = asString(a.path)
      const offset = asNumber(a.offset) ?? 1
      const content = asString(r.content)
      const lines = content ? countLines(content) : 0
      const end = lines > 0 ? offset + lines - 1 : null
      return end ? `${shortenPath(path)}:${offset}-${end}` : shortenPath(path)
    }
    case 'write': {
      const bytes = asNumber(r.bytes)
      return `${shortenPath(asString(a.path))}${bytes != null ? ` · ${bytes}B` : ''}`
    }
    case 'edit': {
      const path = shortenPath(asString(a.path))
      const mode = asString(a.mode)
      let dels = 0
      let adds = 0
      if (mode === 'replace') {
        dels = countLines(asString(a.find))
        adds = countLines(asString(a.replace))
      } else if (mode === 'insert') {
        adds = countLines(asString(a.text))
      } else if (mode === 'delete') {
        dels = countLines(asString(a.find))
      }
      return `${path} · -${dels} +${adds}`
    }
    case 'bash': {
      // Prefer the result's echoed summary (it survives task_id polls);
      // fall back to args.summary on the start frame, then to the first
      // line of the command for legacy frames that predate summary.
      const sum = asString(r.summary) || asString(a.summary)
      if (sum) return sum
      const cmd = asString(a.command)
      if (cmd) return truncate(cmd.split('\n').find((l) => l.trim()) ?? '', 60)
      return asString(a.task_id)
    }
    case 'bash_input': {
      const tid = asString(a.task_id)
      const n = asNumber(r.bytes_written) ?? countLines(asString(a.data))
      return `${tid.slice(0, 14)}${n != null ? ` · ${n}B` : ''}`
    }
    case 'webfetch':
      return shortenUrl(asString(a.url))
    case 'grep': {
      const p = asString(a.pattern)
      const n = asNumber(r.count) ?? 0
      const trunc = r.truncated ? '+' : ''
      return `${truncate(p, 40)} · ${n}${trunc} match${n === 1 ? '' : 'es'}`
    }
    case 'glob': {
      const p = asString(a.pattern)
      const n = asNumber(r.count) ?? 0
      return `${truncate(p, 40)} · ${n} file${n === 1 ? '' : 's'}`
    }
    default:
      return ''
  }
}

// toolBadge is the colored pill at the right end of a tool-call row —
// the part the user scans for "did this succeed". We only set it for
// tools whose result has a clear pass/fail/in-flight signal; otherwise
// the chip is just the summary text.
interface ToolBadge { text: string; cls: string }

function toolBadge(m: AgentMessage): ToolBadge | null {
  const r = payloadResult(m)
  switch (m.tool_name) {
    case 'bash': {
      if (r.timed_out) return { text: 'timed out', cls: 'bg-destructive/15 text-destructive' }
      const s = asString(r.status)
      if (s === 'running') return { text: 'running', cls: 'bg-blue-500/15 text-blue-700 dark:text-blue-300' }
      if (s === 'promoted') return { text: 'promoted', cls: 'bg-amber-500/15 text-amber-700 dark:text-amber-300' }
      const exit = asNumber(r.exit_code)
      if (exit === 0) return { text: 'exit 0', cls: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300' }
      if (exit != null) return { text: `exit ${exit}`, cls: 'bg-destructive/15 text-destructive' }
      return null
    }
    case 'webfetch': {
      const s = asNumber(r.status)
      if (s == null) return null
      const cls = s >= 200 && s < 300
        ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
        : s >= 400
          ? 'bg-destructive/15 text-destructive'
          : 'bg-muted text-muted-foreground'
      return { text: String(s), cls }
    }
    case 'edit': {
      const occ = asNumber(payloadResult(m).occurrences)
      if (occ == null || occ <= 1) return null
      return { text: `×${occ}`, cls: 'bg-muted text-muted-foreground' }
    }
    default:
      return null
  }
}

// isToolCallFailed checks whether a tool call produced an error result.
// It combines tool-specific signals (bash non-zero exit, webfetch >= 400)
// with generic error indicators (error / isError /
// ok === false fields in the result) so that any failing tool call gets
// highlighted without needing a per-tool case.
function isToolCallFailed(m: AgentMessage): boolean {
  const r = payloadResult(m)

  // Generic signals: explicit error/ok fields in the result payload.
  if (r.error != null || r.isError === true || r.ok === false) return true

  // Tool-specific signals that already exist in toolBadge.
  switch (m.tool_name) {
    case 'bash': {
      if (r.timed_out) return true
      const exit = asNumber(r.exit_code)
      if (exit != null && exit !== 0) return true
      return false
    }
    case 'webfetch': {
      const s = asNumber(r.status)
      return s != null && s >= 400
    }
    case 'edit': {
      // The edit tool may signal failure with error in result.
      return false
    }
    default:
      return false
  }
}

// toolErrorMessage returns a short error summary for the collapsed-row
// badge, extracted from the result payload.
function toolErrorMessage(m: AgentMessage): string {
  const r = payloadResult(m)
  // Prefer explicit error field.
  const err = asString(r.error)
  if (err) return truncate(err, 40)
  // Bash: timed_out takes priority.
  if (m.tool_name === 'bash' && r.timed_out) return 'timed out'
  return ''
}

// editDiffLines renders the args of an edit into a unified-diff-style
// preview the expanded view can iterate over. For 'replace' it stacks
// the find lines (red) on top of the replace lines (green); 'insert' is
// pure additions; 'delete' is pure removals. The text is shown verbatim
// — no syntax highlighting — because the LLM operates on whitespace-
// sensitive substrings and we want the user to see exactly what was
// matched.
interface DiffLine { id: string; type: 'add' | 'del' | 'ctx'; text: string }

function editDiffLines(m: AgentMessage): DiffLine[] {
  const a = payloadArgs(m)
  const r = payloadResult(m)

  // Prefer server-returned diff/patch from the result payload.
  const resultDiff = asString(r.diff) || asString(r.patch)
  if (resultDiff) {
    const out: DiffLine[] = []
    let n = 0
    for (const line of resultDiff.split('\n')) {
      const first = line.charAt(0)
      if (first === '+') out.push({ id: `add-${n++}`, type: 'add', text: line.slice(1) })
      else if (first === '-') out.push({ id: `del-${n++}`, type: 'del', text: line.slice(1) })
      else if (first === '@') out.push({ id: `ctx-${n++}`, type: 'ctx', text: line })
      else out.push({ id: `ctx-${n++}`, type: 'ctx', text: line })
    }
    return out
  }

  // Fallback: assemble diff from args (find/replace/text).
  const mode = asString(a.mode)
  const out: DiffLine[] = []
  let n = 0
  const push = (type: DiffLine['type'], text: string) => {
    for (const line of text.split('\n')) out.push({ id: `${type}-${n++}`, type, text: line })
  }
  if (mode === 'replace') {
    push('del', asString(a.find))
    push('add', asString(a.replace))
  } else if (mode === 'insert') {
    const after = asNumber(a.after) ?? 0
    out.push({ id: `ctx-${n++}`, type: 'ctx', text: `@@ after line ${after} @@` })
    push('add', asString(a.text))
  } else if (mode === 'delete') {
    push('del', asString(a.find))
  }
  return out
}

// readResultContent returns the file content the read tool emitted.
// The agent's read tool prefixes each line with "lineno\t...", so the
// text already looks like a numbered listing; we just hand it back
// verbatim for the <pre>.
function readResultContent(m: AgentMessage): string {
  return asString(payloadResult(m).content)
}

// bashFooter is the single status line shown under the terminal output:
// exit code, elapsed status, output-file pointer for background jobs.
function bashFooter(m: AgentMessage): string {
  const r = payloadResult(m)
  const bits: string[] = []
  if (r.timed_out) bits.push('timed out')
  const status = asString(r.status)
  if (status) bits.push(`status=${status}`)
  const exit = asNumber(r.exit_code)
  if (exit != null) bits.push(`exit=${exit}`)
  const tid = asString(r.task_id)
  if (tid) bits.push(`task=${tid}`)
  const file = asString(r.output_file)
  if (file) bits.push(`log=${file}`)
  return bits.join(' · ')
}

function webfetchBody(m: AgentMessage): string {
  const r = payloadResult(m)
  return asString(r.markdown) || asString(r.body) || asString(r.conversion_error)
}

function grepMatches(m: AgentMessage): string[] {
  const r = payloadResult(m)
  const arr = r.matches
  if (Array.isArray(arr)) return arr.map((x) => asString(x))
  return []
}

function globPaths(m: AgentMessage): string[] {
  const r = payloadResult(m)
  const arr = r.paths
  if (Array.isArray(arr)) return arr.map((x) => asString(x))
  return []
}


function messageIcon(kind: string) {
  switch (kind) {
    case 'event':
      return Megaphone
    case 'message':
      return Sparkles
    case 'tool_call':
      return Hammer
    case 'status':
      return Cog
    case 'log':
      return FileText
    case 'done':
      return Square
    case 'system':
      return Bot
    default:
      return Bot
  }
}

// Low-level frames (status pings, log notes, idle markers, internal
// events) are noise for the human reader by default — they fire once
// per turn boundary and are useful only when debugging the runtime
// itself. We hide them behind a toggle so the normal log view stays
// focused on the assistant's thoughts and tool calls.
const showVerbose = ref(false)
const verboseKinds = new Set(['status', 'log', 'event', 'done', 'system'])

const visibleMessages = computed<AgentMessage[]>(() => {
  if (showVerbose.value) return messages.value
  return messages.value.filter((m) => !verboseKinds.has(m.kind))
})

const hiddenVerboseCount = computed(() => {
  if (showVerbose.value) return 0
  return messages.value.filter((m) => verboseKinds.has(m.kind)).length
})

// ---- log container scroll / auto-scroll ----

const logContainer = ref<HTMLElement | null>(null)
// Auto-scroll sticks the log view to the bottom on each new message. The
// user opts out by scrolling up: the scroll handler flips this to false
// when the viewport drifts away from the bottom, and back to true when
// the user scrolls back to the bottom. A floating "scroll to bottom"
// button re-engages it explicitly.
const autoScroll = ref(true)

function isAtBottom(el: HTMLElement, fudge = 24): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight <= fudge
}

function onLogScroll() {
  const el = logContainer.value
  if (!el) return
  autoScroll.value = isAtBottom(el)
}

function scrollToBottom(smooth = true) {
  const el = logContainer.value
  if (!el) return
  el.scrollTo({ top: el.scrollHeight, behavior: smooth ? 'smooth' : 'auto' })
}

// React to message stream growth: when a new frame lands and the user
// hasn't scrolled away, glue to the bottom. We watch `messages.length`
// + `selectedId` so a session switch also lands at the bottom of the
// new log instead of inheriting the previous scroll position.
watch(
  () => [selectedId.value, messages.value.length] as [number | null, number],
  async (_curr, prev) => {
    await nextTick()
    const prevSid = prev ? prev[0] : null
    if (prevSid !== selectedId.value) {
      // Selection changed — always jump to the bottom of the new log
      // and re-arm auto-scroll.
      autoScroll.value = true
      scrollToBottom(false)
      return
    }
    if (autoScroll.value) scrollToBottom(true)
  },
  { flush: 'post' },
)

// ---- session control actions (stop / resume / delete) ----

const stopBusy = ref<Set<number>>(new Set())
const resumeBusy = ref<Set<number>>(new Set())
const deleteBusy = ref<Set<number>>(new Set())

function busyIn(set: Set<number>, id: number): boolean {
  return set.has(id)
}
function setBusy(set: Set<number>, id: number, on: boolean) {
  if (on) set.add(id)
  else set.delete(id)
  // trigger reactivity
  if (set === stopBusy.value) stopBusy.value = new Set(set)
  if (set === resumeBusy.value) resumeBusy.value = new Set(set)
  if (set === deleteBusy.value) deleteBusy.value = new Set(set)
}

async function stopSession(s: AgentSession) {
  if (!confirm(t('agentSessions.actions.stopConfirm'))) return
  setBusy(stopBusy.value, s.session_id, true)
  try {
    await $fetch(`${baseUrl.value}/${s.session_id}/stop`, {
      method: 'POST',
      body: { reason: 'stopped by user' },
    })
    await refresh()
  } catch (e: unknown) {
    const msg = (e as { data?: { error?: string } })?.data?.error ?? t('agentSessions.actions.stopFailed')
    error.value = String(msg)
  } finally {
    setBusy(stopBusy.value, s.session_id, false)
  }
}

async function resumeSession(s: AgentSession) {
  setBusy(resumeBusy.value, s.session_id, true)
  try {
    await $fetch(`${baseUrl.value}/${s.session_id}/resume`, { method: 'POST' })
    await refresh()
  } catch (e: unknown) {
    const msg = (e as { data?: { error?: string } })?.data?.error ?? t('agentSessions.actions.resumeFailed')
    error.value = String(msg)
  } finally {
    setBusy(resumeBusy.value, s.session_id, false)
  }
}

async function deleteSession(s: AgentSession) {
  if (!confirm(t('agentSessions.actions.deleteConfirm'))) return
  setBusy(deleteBusy.value, s.session_id, true)
  try {
    await $fetch(`${baseUrl.value}/${s.session_id}`, { method: 'DELETE' })
    // If the deleted session was selected, clear it; refresh repopulates.
    if (selectedId.value === s.session_id) {
      selectedId.value = null
      messages.value = []
    }
    await refresh()
  } catch (e: unknown) {
    const msg = (e as { data?: { error?: string } })?.data?.error ?? t('agentSessions.actions.deleteFailed')
    error.value = String(msg)
  } finally {
    setBusy(deleteBusy.value, s.session_id, false)
  }
}
</script>

<template>
  <div class="space-y-3">
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <Card v-if="loading && sessions.length === 0" class="gap-0">
      <CardContent class="space-y-2 p-4">
        <Skeleton class="h-4 w-1/3" />
        <Skeleton class="h-4 w-1/2" />
        <Skeleton class="h-4 w-2/5" />
      </CardContent>
    </Card>

    <Card v-else-if="sessions.length === 0" class="gap-0">
      <CardContent class="flex flex-col items-center gap-2 p-8 text-center text-sm text-muted-foreground">
        <Bot class="size-10 opacity-40" />
        <p>{{ t('agentSessions.empty') }}</p>
        <p class="text-xs">{{ t('agentSessions.emptyHint') }}</p>
      </CardContent>
    </Card>

    <div v-else class="grid gap-3 lg:grid-cols-[260px_minmax(0,1fr)]">
      <!-- ─── left pane: session list ───────────────────────────── -->
      <Card class="gap-0 py-0">
        <CardContent class="max-h-48 overflow-y-auto p-0 lg:max-h-none">
          <ul class="divide-y">
            <li
              v-for="s in sessions"
              :key="s.session_id"
              class="cursor-pointer px-3 py-2 hover:bg-muted/40"
              :class="{ 'bg-muted/60': s.session_id === selectedId }"
              @click="selectedId = s.session_id"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="flex min-w-0 items-center gap-1 truncate text-sm font-medium">
                  <AlertTriangle
                    v-if="s.error_message || (s.exit_code != null && s.exit_code !== 0)"
                    class="size-3 shrink-0 text-destructive"
                  />
                  <span class="truncate">{{ s.role_key }}</span>
                </span>
                <Badge :variant="statusVariant(s.status)" class="text-[10px]">
                  <span
                    v-if="isLive(s.status)"
                    class="mr-1 size-1.5 rounded-full bg-current opacity-70 animate-pulse"
                  />
                  {{ s.status }}
                </Badge>
              </div>
              <div class="mt-1 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span class="truncate">{{ causeLabel(s) }}</span>
                <span>{{ duration(s) }}</span>
              </div>
            </li>
          </ul>
        </CardContent>
      </Card>

      <!-- ─── right pane: selected session detail ───────────────── -->
      <Card v-if="selected" class="gap-0 py-0">
        <CardContent class="space-y-4 p-4">
          <!-- Identity strip -->
          <header class="flex flex-wrap items-start justify-between gap-3 border-b pb-3">
            <div class="min-w-0">
              <h3 class="flex items-center gap-2 text-base font-semibold">
                <Bot class="size-4" /> {{ selected.role_key }}
              </h3>
              <p class="mt-1 text-xs text-muted-foreground">
                {{ t('agentSessions.spawnedBy') }}: {{ causeLabel(selected) }}
              </p>
            </div>
            <div class="flex flex-wrap items-center gap-2">
              <Badge :variant="statusVariant(selected.status)">
                {{ selected.status }}
              </Badge>
              <!-- Stop: visible while the container could still be running. -->
              <Button
                v-if="isLive(selected.status)"
                variant="outline"
                size="sm"
                class="h-7 px-2 text-xs"
                :disabled="busyIn(stopBusy, selected.session_id)"
                @click="stopSession(selected)"
              >
                <Square class="size-3.5" />
                {{ busyIn(stopBusy, selected.session_id) ? t('agentSessions.actions.stopping') : t('agentSessions.actions.stop') }}
              </Button>
              <!-- Resume + Delete: visible when the session is failed. -->
              <template v-if="selected.status === 'failed'">
                <Button
                  variant="outline"
                  size="sm"
                  class="h-7 px-2 text-xs"
                  :disabled="busyIn(resumeBusy, selected.session_id)"
                  @click="resumeSession(selected)"
                >
                  <Play class="size-3.5" />
                  {{ busyIn(resumeBusy, selected.session_id) ? t('agentSessions.actions.resuming') : t('agentSessions.actions.resume') }}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  class="h-7 px-2 text-xs text-destructive hover:text-destructive"
                  :disabled="busyIn(deleteBusy, selected.session_id)"
                  @click="deleteSession(selected)"
                >
                  <Trash2 class="size-3.5" />
                  {{ busyIn(deleteBusy, selected.session_id) ? t('agentSessions.actions.deleting') : t('agentSessions.actions.delete') }}
                </Button>
              </template>
              <!-- Delete is also allowed on idle / succeeded / cancelled — anything not live. -->
              <Button
                v-else-if="!isLive(selected.status) && selected.status !== 'archived' && selected.status !== 'failed'"
                variant="ghost"
                size="sm"
                class="h-7 px-2 text-xs text-destructive hover:text-destructive"
                :disabled="busyIn(deleteBusy, selected.session_id)"
                @click="deleteSession(selected)"
              >
                <Trash2 class="size-3.5" />
                {{ busyIn(deleteBusy, selected.session_id) ? t('agentSessions.actions.deleting') : t('agentSessions.actions.delete') }}
              </Button>
              <!-- Resume is also offered on idle / succeeded so the user can re-trigger without a new comment. -->
              <Button
                v-if="selected.status === 'idle' || selected.status === 'succeeded' || selected.status === 'cancelled'"
                variant="outline"
                size="sm"
                class="h-7 px-2 text-xs"
                :disabled="busyIn(resumeBusy, selected.session_id)"
                @click="resumeSession(selected)"
              >
                <RotateCcw class="size-3.5" />
                {{ busyIn(resumeBusy, selected.session_id) ? t('agentSessions.actions.resuming') : t('agentSessions.actions.resume') }}
              </Button>
            </div>
          </header>

          <dl class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
            <dt class="text-muted-foreground">{{ t('agentSessions.field.image') }}</dt>
            <dd class="truncate font-mono">{{ selected.role_config?.container?.image || '—' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.model') }}</dt>
            <dd class="truncate font-mono">{{ selected.role_config?.model || '—' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.repoSha') }}</dt>
            <dd class="font-mono">{{ shortSha(selected.repo_sha) || '—' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.permission') }}</dt>
            <dd class="font-mono">{{ selected.role_config?.permission || 'read' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.tools') }}</dt>
            <dd class="flex flex-wrap gap-1">
              <Badge
                v-for="tool in selected.role_config?.tool_patterns ?? []"
                :key="tool"
                variant="outline"
                class="text-[10px]"
              >{{ tool }}</Badge>
              <span v-if="!(selected.role_config?.tool_patterns ?? []).length" class="text-muted-foreground">—</span>
            </dd>
          </dl>

          <!-- Failure detail. Shown whenever the row carries an error message
               (set by the runner on terminate) or terminated non-success. -->
          <div
            v-if="selected.error_message || (selected.exit_code != null && selected.exit_code !== 0) || selected.status === 'failed' || selected.status === 'cancelled'"
            class="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-xs"
          >
            <div class="mb-1 flex items-center gap-1.5 font-medium text-destructive">
              <AlertTriangle class="size-3.5" />
              {{ t('agentSessions.failureTitle') }}
            </div>
            <div class="space-y-0.5">
              <div v-if="selected.exit_code != null">
                <span class="text-muted-foreground">{{ t('agentSessions.field.exitCode') }}:</span>
                <code class="ml-1 font-mono">{{ selected.exit_code }}</code>
              </div>
              <div v-if="selected.error_message">
                <span class="text-muted-foreground">{{ t('agentSessions.field.errorMessage') }}:</span>
                <pre class="mt-1 whitespace-pre-wrap wrap-break-word rounded bg-muted/30 p-2 font-mono text-[11px]">{{ selected.error_message }}</pre>
              </div>
              <p v-else-if="!selected.exit_code" class="text-muted-foreground">
                {{ t('agentSessions.failureNoDetail') }}
              </p>
            </div>
          </div>

          <!-- Message log. The list itself is the scroll viewport so a
               long stream never pushes the rest of the issue page out of
               view; the relative wrapper anchors the floating
               "scroll to bottom" affordance. -->
          <div>
            <div class="mb-2 flex items-center justify-between gap-2">
              <h4 class="text-xs font-semibold uppercase text-muted-foreground">
                {{ t('agentSessions.messages') }}
              </h4>
              <div class="flex items-center gap-3 text-[10px] text-muted-foreground">
                <button
                  v-if="hiddenVerboseCount > 0 || showVerbose"
                  class="hover:text-foreground"
                  @click="showVerbose = !showVerbose"
                >
                  {{ showVerbose
                    ? t('agentSessions.actions.hideVerbose')
                    : t('agentSessions.actions.showVerbose', { n: hiddenVerboseCount }) }}
                </button>
                <span v-if="autoScroll && isLive(selected.status)">
                  {{ t('agentSessions.actions.autoScroll') }}
                </span>
              </div>
            </div>
            <p v-if="messages.length === 0" class="text-xs text-muted-foreground">
              {{ t('agentSessions.messagesEmpty') }}
            </p>
            <div v-else class="relative">
              <ol
                ref="logContainer"
                class="max-h-128 overflow-y-auto rounded border bg-background/40 p-1 text-sm divide-y divide-border/30"
                @scroll.passive="onLogScroll"
              >
                <li
                  v-for="m in visibleMessages"
                  :key="m.id"
                  class="text-sm"
                >
                  <!-- ─── Tool call: header + tool-aware expanded body ─ -->
                  <template v-if="m.kind === 'tool_call'">
                    <button
                      type="button"
                      class="group flex w-full flex-wrap items-center gap-x-2 gap-y-0.5 px-2 py-1 text-left hover:bg-muted/40"
                      :class="{ 'bg-destructive/5 hover:bg-destructive/10 ring-1 ring-inset ring-destructive/15': isToolCallFailed(m) }"
                      @click="toggleExpand(m.seq)"
                    >
                      <ChevronRight
                        class="size-3 shrink-0 text-muted-foreground transition-transform"
                        :class="{ 'rotate-90': isExpanded(m.seq) }"
                      />
                      <component :is="toolIcon(m.tool_name)" class="size-3.5 shrink-0" :class="isToolCallFailed(m) ? 'text-destructive' : 'text-muted-foreground'" />
                      <span class="hidden shrink-0 font-mono text-[10px] text-muted-foreground sm:inline">{{ offsetFrom(selected.created_at, m.created_at) }}</span>
                      <span class="shrink-0 text-xs font-semibold" :class="{ 'text-destructive': isToolCallFailed(m) }">{{ m.tool_name }}</span>
                      <span class="min-w-0 truncate font-mono text-xs" :class="isToolCallFailed(m) ? 'text-destructive/80' : 'text-muted-foreground'">{{ toolSummary(m) }}</span>
                      <!-- Error badge: shown when isToolCallFailed, with optional error message -->
                      <span
                        v-if="isToolCallFailed(m)"
                        class="ml-auto shrink-0 rounded px-1.5 py-0.5 font-mono text-[10px] bg-destructive/15 text-destructive"
                      >
                        <XCircle class="mr-0.5 inline size-2.5 align-[-1px]" />
                        {{ toolErrorMessage(m) || 'error' }}
                      </span>
                      <!-- Normal tool badge: only shown when NOT failed, to avoid double-badging -->
                      <span
                        v-else-if="toolBadge(m)"
                        class="ml-auto shrink-0 rounded px-1.5 py-0.5 font-mono text-[10px]"
                        :class="toolBadge(m)!.cls"
                      >{{ toolBadge(m)!.text }}</span>
                    </button>

                    <div v-if="isExpanded(m.seq)" class="border-t bg-background/60">
                      <!-- Error detail: shown at the top when tool call failed -->
                      <div
                        v-if="isToolCallFailed(m)"
                        class="border-b border-destructive/20 bg-destructive/5 px-3 py-2"
                      >
                        <div class="mb-1 flex items-center gap-1.5 text-xs font-medium text-destructive">
                          <AlertTriangle class="size-3.5" />
                          {{ t('agentSessions.toolError') }}
                        </div>
                        <pre
                          v-if="payloadResult(m).error"
                          class="mt-1 whitespace-pre-wrap break-all rounded bg-destructive/10 p-2 font-mono text-[11px] text-destructive"
                        >{{ payloadResult(m).error }}</pre>
                        <p v-else class="text-xs text-muted-foreground">
                          {{ t('agentSessions.toolErrorNoDetail') }}
                        </p>
                      </div>

                      <!-- read: numbered content (pre-formatted by the tool) -->
                      <template v-if="m.tool_name === 'read'">
                        <pre class="max-h-96 overflow-auto whitespace-pre-wrap break-all bg-zinc-950 p-2 font-mono text-[11px] leading-relaxed text-zinc-100">{{ readResultContent(m) || '(empty)' }}</pre>
                      </template>

                      <!-- edit: inline find/replace diff -->
                      <template v-else-if="m.tool_name === 'edit'">
                        <template v-if="editDiffLines(m).length > 0">
                          <div class="space-y-0.5 px-2 py-2 font-mono text-[11px]">
                            <div
                              v-for="line in editDiffLines(m)"
                              :key="line.id"
                              class="whitespace-pre-wrap wrap-break-word px-1"
                              :class="line.type === 'add'
                                ? 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
                                : line.type === 'del'
                                  ? 'bg-destructive/10 text-destructive line-through decoration-destructive/60'
                                  : 'text-muted-foreground'"
                            ><span class="mr-1 inline-block w-3 select-none opacity-70">{{ line.type === 'add' ? '+' : line.type === 'del' ? '-' : ' ' }}</span>{{ line.text }}</div>
                          </div>
                        </template>
                        <div v-else class="px-2 py-3 text-center text-xs text-muted-foreground">
                          {{ t('agentSessions.noEditDiff') }}
                        </div>
                      </template>

                      <!-- bash: $ command + terminal output + footer -->
                      <template v-else-if="m.tool_name === 'bash'">
                        <div class="space-y-1 px-2 py-2">
                          <div v-if="payloadArgs(m).command" class="break-all font-mono text-[11px] text-muted-foreground">
                            <span class="mr-1 opacity-60">$</span>{{ payloadArgs(m).command }}
                          </div>
                          <div v-else-if="payloadArgs(m).task_id" class="font-mono text-[11px] text-muted-foreground">
                            poll {{ payloadArgs(m).task_id }}
                          </div>
                          <pre class="max-h-96 overflow-auto rounded bg-zinc-950 p-2 font-mono text-[11px] leading-relaxed text-zinc-100 whitespace-pre-wrap wrap-break-word">{{ payloadResult(m).output || '(no output)' }}</pre>
                          <div v-if="bashFooter(m)" class="font-mono text-[10px] text-muted-foreground">{{ bashFooter(m) }}</div>
                        </div>
                      </template>

                      <!-- write: content preview with byte count -->
                      <template v-else-if="m.tool_name === 'write'">
                        <div class="space-y-1 px-2 py-2">
                          <div class="font-mono text-[10px] text-muted-foreground">{{ payloadArgs(m).path }}<span v-if="payloadResult(m).bytes"> · {{ payloadResult(m).bytes }} B</span></div>
                          <pre class="max-h-96 overflow-auto rounded bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap wrap-break-word">{{ payloadArgs(m).content }}</pre>
                        </div>
                      </template>

                      <!-- webfetch: header + markdown/body -->
                      <template v-else-if="m.tool_name === 'webfetch'">
                        <div class="space-y-1 px-2 py-2">
                          <div class="break-all font-mono text-[10px] text-muted-foreground">
                            {{ payloadArgs(m).url }}<span v-if="payloadResult(m).content_type"> · {{ payloadResult(m).content_type }}</span>
                          </div>
                          <pre class="max-h-96 overflow-auto rounded bg-muted/40 p-2 text-[11px] whitespace-pre-wrap wrap-break-word">{{ webfetchBody(m) || '(empty)' }}</pre>
                        </div>
                      </template>

                      <!-- grep: matches list, one per line -->
                      <template v-else-if="m.tool_name === 'grep'">
                        <div class="px-2 py-2">
                          <pre class="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/40 p-2 font-mono text-[11px]">{{ grepMatches(m).join('\n') || '(no matches)' }}</pre>
                        </div>
                      </template>

                      <!-- glob: paths list -->
                      <template v-else-if="m.tool_name === 'glob'">
                        <div class="px-2 py-2">
                          <pre class="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/40 p-2 font-mono text-[11px]">{{ globPaths(m).join('\n') || '(no paths)' }}</pre>
                        </div>
                      </template>

                      <!-- Fallback: pretty-printed args + result -->
                      <template v-else>
                        <div class="space-y-2 px-2 py-2">
                          <div>
                            <p class="text-[10px] uppercase text-muted-foreground">args</p>
                            <pre class="mt-0.5 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/40 p-2 text-[11px]">{{ JSON.stringify(payloadArgs(m), null, 2) }}</pre>
                          </div>
                          <div>
                            <p class="text-[10px] uppercase text-muted-foreground">result</p>
                            <pre class="mt-0.5 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/40 p-2 text-[11px]">{{ JSON.stringify(payloadResult(m), null, 2) }}</pre>
                          </div>
                        </div>
                      </template>
                    </div>
                  </template>

                  <!-- ─── Assistant / user text bubble ─────────────── -->
                  <template v-else-if="m.kind === 'message'">
                    <div class="flex items-start gap-2 px-2 py-1.5">
                      <Sparkles class="mt-0.5 size-3.5 shrink-0 text-primary" />
                      <span class="shrink-0 font-mono text-[10px] text-muted-foreground">{{ offsetFrom(selected.created_at, m.created_at) }}</span>
                      <p class="min-w-0 flex-1 whitespace-pre-wrap wrap-break-word">{{ m.content }}</p>
                    </div>
                  </template>

                  <!-- ─── Low-level frames (status/log/event/done/sys) ─ -->
                  <template v-else>
                    <button
                      type="button"
                      class="flex w-full items-center gap-2 px-2 py-0.5 text-left text-[11px] text-muted-foreground hover:bg-muted/30"
                      :disabled="!m.payload"
                      @click="m.payload && toggleExpand(m.seq)"
                    >
                      <ChevronRight
                        v-if="m.payload"
                        class="size-3 shrink-0 transition-transform"
                        :class="{ 'rotate-90': isExpanded(m.seq) }"
                      />
                      <span v-else class="inline-block size-3 shrink-0" />
                      <component :is="messageIcon(m.kind)" class="size-3 shrink-0" />
                      <span class="font-mono text-[10px]">{{ offsetFrom(selected.created_at, m.created_at) }}</span>
                      <span>{{ m.kind }}</span>
                      <span v-if="m.event" class="truncate font-mono">{{ m.event }}</span>
                      <span v-else-if="m.content" class="truncate">{{ m.content }}</span>
                    </button>
                    <pre
                      v-if="isExpanded(m.seq) && m.payload"
                      class="mx-2 mb-1 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/40 p-2 text-[11px]"
                    >{{ payloadString(m) }}</pre>
                  </template>
                </li>
              </ol>
              <Button
                v-if="!autoScroll"
                variant="secondary"
                size="sm"
                class="absolute bottom-3 right-3 h-7 gap-1 px-2 text-xs shadow"
                @click="autoScroll = true; scrollToBottom(true)"
              >
                <ArrowDownToLine class="size-3.5" />
                {{ t('agentSessions.actions.scrollToBottom') }}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
