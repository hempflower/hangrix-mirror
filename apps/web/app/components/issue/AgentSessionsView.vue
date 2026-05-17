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

import { computed, onUnmounted, ref, watch } from 'vue'
import { AlertTriangle, Bot, Cog, FileText, Hammer, Megaphone, Sparkles, Square } from 'lucide-vue-next'
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
    can?: string[]
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

function relTime(iso: string) {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!t) return iso
  const diff = Math.max(0, Math.floor((Date.now() - t) / 1000))
  if (diff < 60) return `${diff}s`
  if (diff < 3600) return `${Math.floor(diff / 60)}m`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`
  return `${Math.floor(diff / 86400)}d`
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

function payloadField(m: AgentMessage, key: string): string {
  const p = m.payload as Record<string, unknown> | null
  if (!p || typeof p !== 'object') return ''
  const v = p[key]
  if (v == null) return ''
  if (typeof v === 'string') return v
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
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
        <CardContent class="p-0">
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
          <header class="flex items-start justify-between gap-3 border-b pb-3">
            <div class="min-w-0">
              <h3 class="flex items-center gap-2 text-base font-semibold">
                <Bot class="size-4" /> {{ selected.role_key }}
              </h3>
              <p class="mt-1 text-xs text-muted-foreground">
                {{ t('agentSessions.spawnedBy') }}: {{ causeLabel(selected) }}
              </p>
            </div>
            <Badge :variant="statusVariant(selected.status)">
              {{ selected.status }}
            </Badge>
          </header>

          <dl class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
            <dt class="text-muted-foreground">{{ t('agentSessions.field.image') }}</dt>
            <dd class="truncate font-mono">{{ selected.role_config?.container?.image || '—' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.model') }}</dt>
            <dd class="truncate font-mono">{{ selected.role_config?.model || '—' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.repoSha') }}</dt>
            <dd class="font-mono">{{ shortSha(selected.repo_sha) || '—' }}</dd>
            <dt class="text-muted-foreground">{{ t('agentSessions.field.can') }}</dt>
            <dd class="flex flex-wrap gap-1">
              <Badge
                v-for="tool in selected.role_config?.can ?? []"
                :key="tool"
                variant="outline"
                class="text-[10px]"
              >{{ tool }}</Badge>
              <span v-if="!(selected.role_config?.can ?? []).length" class="text-muted-foreground">—</span>
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

          <!-- Message log -->
          <div>
            <h4 class="mb-2 text-xs font-semibold uppercase text-muted-foreground">
              {{ t('agentSessions.messages') }}
            </h4>
            <p v-if="messages.length === 0" class="text-xs text-muted-foreground">
              {{ t('agentSessions.messagesEmpty') }}
            </p>
            <ol v-else class="space-y-2 text-sm">
              <li
                v-for="m in messages"
                :key="m.id"
                class="rounded border bg-muted/20 px-3 py-2"
              >
                <div class="flex items-center gap-2 text-xs text-muted-foreground">
                  <component :is="messageIcon(m.kind)" class="size-3.5 shrink-0" />
                  <span class="font-mono">{{ offsetFrom(selected.created_at, m.created_at) }}</span>
                  <span class="font-medium text-foreground">{{ m.kind }}</span>
                  <span v-if="m.tool_name" class="font-mono">{{ m.tool_name }}</span>
                  <span v-else-if="m.event" class="font-mono">{{ m.event }}</span>
                  <span v-else-if="m.role" class="font-mono">{{ m.role }}</span>
                  <Button
                    v-if="m.payload"
                    variant="ghost"
                    size="sm"
                    class="ml-auto h-6 px-2 text-xs"
                    @click="toggleExpand(m.seq)"
                  >{{ isExpanded(m.seq) ? t('agentSessions.collapse') : t('agentSessions.expand') }}</Button>
                </div>
                <!-- assistant / message content rendered as text -->
                <p v-if="m.content" class="mt-1 whitespace-pre-wrap break-words">{{ m.content }}</p>
                <!-- tool_call: show args + result as two panes when expanded -->
                <div v-if="isExpanded(m.seq) && m.kind === 'tool_call'" class="mt-2 space-y-2">
                  <div v-if="payloadField(m, 'args')">
                    <p class="text-[10px] uppercase text-muted-foreground">args</p>
                    <pre class="mt-0.5 overflow-x-auto rounded bg-muted/40 p-2 text-xs">{{ payloadField(m, 'args') }}</pre>
                  </div>
                  <div v-if="payloadField(m, 'result')">
                    <p class="text-[10px] uppercase text-muted-foreground">result</p>
                    <pre class="mt-0.5 overflow-x-auto rounded bg-muted/40 p-2 text-xs">{{ payloadField(m, 'result') }}</pre>
                  </div>
                </div>
                <!-- event / status / log / system: just show the payload raw -->
                <pre
                  v-else-if="isExpanded(m.seq) && m.payload"
                  class="mt-2 overflow-x-auto rounded bg-muted/40 p-2 text-xs"
                >{{ payloadString(m) }}</pre>
              </li>
            </ol>
          </div>
        </CardContent>
      </Card>
    </div>
  </div>
</template>
