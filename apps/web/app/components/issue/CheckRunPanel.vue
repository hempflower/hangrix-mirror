<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  CheckCircle2,
  Circle,
  Loader2,
  XCircle,
  MinusCircle,
} from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import type { WorkflowRun, WorkflowRunStatus } from '~/types/workflow'
import { relativeTime } from '~/utils/time'

const props = defineProps<{
  owner: string
  name: string
  /** Branch ref to filter workflow runs by (e.g. "refs/heads/issue-5/web/foo") */
  branchRef?: string | null
  /** Commit SHA to filter by (preferred when available) */
  headSha?: string | null
}>()

const { t } = useI18n()

const runs = ref<WorkflowRun[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

const shortRef = computed(() => {
  const r = props.branchRef ?? ''
  return r.replace(/^refs\/heads\//, '')
})

async function load() {
  loading.value = true
  error.value = null
  try {
    const params: Record<string, string> = { limit: '30' }
    const res = await $fetch<{ items: WorkflowRun[]; total: number }>(
      `/api/repos/${props.owner}/${props.name}/workflow-runs`,
      { credentials: 'include', query: params },
    )
    let items = res.items ?? []

    // Filter: prefer head_sha, fall back to branch ref name
    if (props.headSha) {
      items = items.filter((r) => r.commit_sha === props.headSha)
    } else if (props.branchRef) {
      const branch = shortRef.value
      items = items.filter((r) => r.ref === branch)
    }

    runs.value = items
  } catch (e: any) {
    error.value = e?.data?.error ?? t('issue.contributions.checksLoadFailed')
    runs.value = []
  } finally {
    loading.value = false
  }
}

// --- polling ---
const POLL_MS = 5_000
let timer: ReturnType<typeof setInterval> | null = null

function startPoll() {
  if (timer || typeof window === 'undefined') return
  timer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    load()
  }, POLL_MS)
}

function stopPoll() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

onMounted(() => {
  load()
  startPoll()
})

onUnmounted(() => stopPoll())

// Re-load when filter props change
watch(
  () => [props.branchRef, props.headSha],
  () => { load() },
)

// --- helpers ---

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function shortSha(s: string) {
  return s ? s.slice(0, 7) : ''
}

function duration(started: string | null, finished: string | null): string {
  if (!started) return '—'
  const start = Date.parse(started)
  if (Number.isNaN(start)) return '—'
  const end = finished ? Date.parse(finished) : Date.now()
  if (Number.isNaN(end)) return '—'
  const diffSec = Math.max(0, Math.round((end - start) / 1000))
  if (diffSec < 60) return `${diffSec}s`
  const min = Math.floor(diffSec / 60)
  const sec = diffSec % 60
  if (min < 60) return `${min}m ${sec}s`
  const hr = Math.floor(min / 60)
  return `${hr}h ${Math.floor(min % 60)}m`
}

function statusIcon(s: WorkflowRunStatus) {
  switch (s) {
    case 'success': return CheckCircle2
    case 'failed': return XCircle
    case 'running': return Loader2
    case 'cancelled': return MinusCircle
    default: return Circle
  }
}

function statusIconClass(s: WorkflowRunStatus): string {
  switch (s) {
    case 'success': return 'text-emerald-500'
    case 'failed': return 'text-red-500'
    case 'running': return 'text-amber-500 animate-spin'
    case 'cancelled': return 'text-slate-400'
    default: return 'text-slate-400'
  }
}

function statusBgClass(s: WorkflowRunStatus): string {
  switch (s) {
    case 'success': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'failed': return 'bg-red-500/15 text-red-700 dark:text-red-300'
    case 'running': return 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
    case 'cancelled': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
    default: return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}

// Aggregate summary counts
const summary = computed(() => {
  let success = 0
  let failed = 0
  let running = 0
  let pending = 0
  let cancelled = 0
  for (const r of runs.value) {
    switch (r.status) {
      case 'success': success++; break
      case 'failed': failed++; break
      case 'running': running++; break
      case 'cancelled': cancelled++; break
      default: pending++; break
    }
  }
  return { success, failed, running, pending, cancelled }
})

const overallVerdict = computed<'success' | 'failure' | 'running' | 'pending' | 'none'>(() => {
  if (runs.value.length === 0) return 'none'
  if (summary.value.failed > 0) return 'failure'
  if (summary.value.running > 0 || summary.value.pending > 0) return 'running'
  if (summary.value.success === runs.value.length) return 'success'
  return 'pending'
})
</script>

<template>
  <div class="space-y-3">
    <!-- Error state -->
    <Card v-if="error" class="gap-0 py-0">
      <CardContent class="p-4 text-sm text-destructive">
        {{ error }}
      </CardContent>
    </Card>

    <!-- Loading -->
    <Card v-else-if="loading" class="gap-0 py-0">
      <CardContent class="flex items-center gap-2 p-4 text-sm text-muted-foreground">
        <Loader2 class="size-4 animate-spin" />
        {{ t('issue.contributions.checksLoading') }}
      </CardContent>
    </Card>

    <!-- Empty -->
    <Card v-else-if="runs.length === 0" class="gap-0 py-0">
      <CardContent class="flex items-center gap-2 p-4 text-sm text-muted-foreground">
        <Circle class="size-4" />
        {{ t('issue.contributions.checksEmpty') }}
      </CardContent>
    </Card>

    <!-- Check list -->
    <template v-else>
      <!-- Summary strip -->
      <div class="flex flex-wrap items-center gap-2 text-xs">
        <!-- Overall verdict badge -->
        <Badge
          v-if="overallVerdict !== 'none'"
          :class="overallVerdict === 'success'
            ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
            : overallVerdict === 'failure'
              ? 'bg-red-500/15 text-red-700 dark:text-red-300'
              : overallVerdict === 'running'
                ? 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
                : 'bg-slate-500/15 text-slate-700 dark:text-slate-300'"
          variant="secondary"
        >
          <component
            :is="overallVerdict === 'success' ? CheckCircle2 : overallVerdict === 'failure' ? XCircle : overallVerdict === 'running' ? Loader2 : Circle"
            :class="overallVerdict === 'running' ? 'animate-spin' : ''"
            class="mr-1 size-3"
          />
          <template v-if="overallVerdict === 'success'">{{ t('issue.contributions.checksAllPassed') }}</template>
          <template v-else-if="overallVerdict === 'failure'">{{ t('issue.contributions.checksSomeFailed') }}</template>
          <template v-else-if="overallVerdict === 'running'">{{ t('issue.contributions.checksRunning') }}</template>
          <template v-else>{{ t('issue.contributions.checksPending') }}</template>
        </Badge>

        <!-- Count chips -->
        <span v-if="summary.success > 0" class="inline-flex items-center gap-1">
          <CheckCircle2 class="size-3 text-emerald-500" />
          <span class="text-emerald-700 dark:text-emerald-300">{{ summary.success }}</span>
        </span>
        <span v-if="summary.failed > 0" class="inline-flex items-center gap-1">
          <XCircle class="size-3 text-red-500" />
          <span class="text-red-700 dark:text-red-300">{{ summary.failed }}</span>
        </span>
        <span v-if="summary.running > 0" class="inline-flex items-center gap-1">
          <Loader2 class="size-3 text-amber-500 animate-spin" />
          <span class="text-amber-700 dark:text-amber-300">{{ summary.running }}</span>
        </span>
        <span v-if="summary.pending > 0 || summary.cancelled > 0" class="inline-flex items-center gap-1">
          <MinusCircle class="size-3 text-slate-400" />
          <span class="text-slate-500">{{ summary.pending + summary.cancelled }}</span>
        </span>
      </div>

      <!-- Run list -->
      <Card class="gap-0 py-0">
        <CardContent class="p-0">
          <ul class="divide-y">
            <li v-for="run in runs" :key="run.id">
              <NuxtLink
                :to="`/${owner}/${name}/workflows/${run.id}`"
                class="flex items-center gap-3 px-4 py-2.5 hover:bg-muted/30 transition-colors"
              >
                <!-- Status icon -->
                <component
                  :is="statusIcon(run.status)"
                  :class="statusIconClass(run.status)"
                  class="size-4 shrink-0"
                />

                <!-- Name + meta -->
                <div class="min-w-0 flex-1 space-y-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="text-sm font-medium truncate">{{ run.workflow_name }}</span>
                    <Badge :class="statusBgClass(run.status)" variant="secondary" class="text-xs">
                      {{ t(`repo.workflows.status.${run.status}`) }}
                    </Badge>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    {{ t(`repo.workflows.event.${run.event_name}`) }}
                    <span class="mx-1">·</span>
                    <code class="font-mono text-[10px]">{{ shortSha(run.commit_sha) }}</code>
                    <template v-if="run.started_at">
                      <span class="mx-1">·</span>
                      {{ duration(run.started_at, run.finished_at) }}
                    </template>
                    <span class="mx-1">·</span>
                    {{ rel(run.created_at) }}
                  </p>
                </div>
              </NuxtLink>
            </li>
          </ul>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
