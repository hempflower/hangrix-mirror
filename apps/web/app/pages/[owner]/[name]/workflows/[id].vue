<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Clock, Play, ScrollText, Terminal, XCircle } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type {
  WorkflowJobLogLine,
  WorkflowJobLogsResp,
  WorkflowJobRun,
  WorkflowJobStatus,
  WorkflowRunDetail,
  WorkflowRunStatus,
} from '~/types/workflow'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const id = computed(() => Number(route.params.id ?? 0))

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.workflows.title'), to: `${base}/workflows` },
    { label: detail.value?.run.workflow_name || `#${id.value}` },
  ]
})

const detail = ref<WorkflowRunDetail | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

// --- Logs ---
const logsJobId = ref<number | null>(null)
const logs = ref<WorkflowJobLogLine[]>([])
const logsLoading = ref(false)
const logsError = ref<string | null>(null)
const logsAutoScroll = ref(true)
const logsContainer = ref<HTMLElement | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    detail.value = await $fetch<WorkflowRunDetail>(`/api/repos/${owner.value}/${name.value}/workflows/runs/${id.value}`, {
      credentials: 'include',
    })
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.workflows.loadFailed')
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function loadLogs(jobId: number) {
  logsJobId.value = jobId
  logsLoading.value = true
  logsError.value = null
  logs.value = []
  try {
    const res = await $fetch<WorkflowJobLogsResp>(`/api/repos/${owner.value}/${name.value}/workflows/runs/${id.value}/jobs/${jobId}/logs`, {
      credentials: 'include',
    })
    logs.value = res.lines ?? []
  } catch (e: any) {
    logsError.value = e?.data?.error ?? t('repo.workflows.job.logsFailed')
  } finally {
    logsLoading.value = false
    // Scroll to bottom after render
    setTimeout(scrollLogsToBottom, 50)
  }
}

function scrollLogsToBottom() {
  if (!logsAutoScroll.value || !logsContainer.value) return
  logsContainer.value.scrollTop = logsContainer.value.scrollHeight
}

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function shortSha(s: string) { return s.slice(0, 7) }

function runStatusVariant(s: WorkflowRunStatus) {
  switch (s) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    case 'running': return 'default' as const
    case 'cancelled': return 'outline' as const
    default: return 'secondary' as const
  }
}

function jobStatusVariant(s: WorkflowJobStatus) {
  switch (s) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    case 'running': return 'default' as const
    case 'cancelled': return 'outline' as const
    case 'skipped': return 'secondary' as const
    default: return 'secondary' as const
  }
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

function streamClass(s: string) {
  switch (s) {
    case 'stderr': return 'text-red-500'
    case 'system': return 'text-muted-foreground'
    default: return ''
  }
}

// --- Cancel ---
const cancelSending = ref(false)

async function onCancel() {
  if (!detail.value) return
  if (!window.confirm(t('repo.workflows.cancelConfirm'))) return
  cancelSending.value = true
  try {
    const updated = await $fetch<WorkflowRunDetail>(`/api/repos/${owner.value}/${name.value}/workflows/runs/${id.value}/cancel`, {
      method: 'POST',
      credentials: 'include',
    })
    detail.value = updated
  } catch (e: any) {
    // eslint-disable-next-line no-alert
    window.alert(e?.data?.error ?? t('repo.workflows.cancelFailed'))
  } finally {
    cancelSending.value = false
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-4xl space-y-6">
    <!-- Loading -->
    <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <!-- Error -->
    <div v-else-if="error || !detail" class="space-y-2">
      <p class="text-sm text-destructive">{{ error || t('repo.workflows.loadFailed') }}</p>
      <Button variant="outline" as-child>
        <NuxtLink :to="`/${owner}/${name}/workflows`">
          {{ t('repo.workflows.title') }}
        </NuxtLink>
      </Button>
    </div>

    <template v-else>
      <!-- Run header -->
      <header class="space-y-3">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="space-y-1">
            <h1 class="text-2xl font-semibold tracking-tight">
              {{ detail.run.workflow_name }}
            </h1>
            <p class="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
              <Badge :variant="runStatusVariant(detail.run.status)">
                {{ t(`repo.workflows.status.${detail.run.status}`) }}
              </Badge>
              <span>{{ t(`repo.workflows.event.${detail.run.event_name}`) }}</span>
              <span>·</span>
              <code class="font-mono text-xs">{{ detail.run.ref }}</code>
              <span>·</span>
              <code class="font-mono text-xs">{{ shortSha(detail.run.commit_sha) }}</code>
              <span>·</span>
              <span>{{ rel(detail.run.created_at) }}</span>
              <template v-if="detail.run.started_at">
                <span>·</span>
                <span>{{ duration(detail.run.started_at, detail.run.finished_at) }}</span>
              </template>
            </p>
          </div>
          <Button
            v-if="detail.run.status === 'pending' || detail.run.status === 'running'"
            variant="outline"
            class="text-destructive hover:text-destructive"
            :disabled="cancelSending"
            @click="onCancel"
          >
            <XCircle class="size-4" />
            {{ t('repo.workflows.cancel') }}
          </Button>
        </div>
      </header>

      <!-- Jobs -->
      <Card>
        <CardHeader class="pb-3">
          <CardTitle class="text-base flex items-center gap-2">
            <Play class="size-4" />
            {{ t('repo.workflows.job.title') }}
          </CardTitle>
        </CardHeader>
        <CardContent class="space-y-3">
          <p v-if="detail.jobs.length === 0" class="text-sm text-muted-foreground">
            {{ t('repo.workflows.job.empty') }}
          </p>
          <div v-else class="divide-y rounded-md border">
            <div
              v-for="job in detail.jobs"
              :key="job.id"
              class="flex flex-wrap items-center gap-3 px-3 py-2.5"
            >
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-medium">{{ job.display_name || job.job_key }}</span>
                  <Badge :variant="jobStatusVariant(job.status)" class="text-xs">
                    {{ t(`repo.workflows.status.${job.status}`) }}
                  </Badge>
                  <span v-if="job.exit_code !== null" class="text-xs text-muted-foreground">
                    exit={{ job.exit_code }}
                  </span>
                  <span v-if="job.error_message" class="text-xs text-red-500">
                    {{ job.error_message }}
                  </span>
                </div>
                <p class="text-xs text-muted-foreground">
                  {{ duration(job.started_at, job.finished_at) }}
                  <span v-if="job.runner_id" class="mx-1">·</span>
                  <span v-if="job.runner_id">runner #{{ job.runner_id }}</span>
                </p>
              </div>
              <Button
                variant="outline"
                size="sm"
                @click="loadLogs(job.id)"
              >
                <ScrollText class="size-3.5" />
                {{ t('repo.workflows.job.logs') }}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <!-- Logs panel -->
      <Card v-if="logsJobId !== null">
        <CardHeader class="pb-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <CardTitle class="text-base flex items-center gap-2">
              <Terminal class="size-4" />
              {{ t('repo.workflows.job.logs') }}
            </CardTitle>
            <div class="flex items-center gap-2">
              <label class="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
                <input
                  v-model="logsAutoScroll"
                  type="checkbox"
                  class="size-3.5 rounded"
                />
                {{ t('agentSessions.actions.autoScroll') }}
              </label>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <p v-if="logsLoading" class="text-sm text-muted-foreground">
            {{ t('repo.workflows.job.logsLoading') }}
          </p>
          <p v-else-if="logsError" class="text-sm text-destructive">
            {{ logsError }}
          </p>
          <div
            v-else-if="logs.length > 0"
            ref="logsContainer"
            class="max-h-96 overflow-auto rounded-md bg-muted/50 p-3 font-mono text-xs leading-relaxed"
          >
            <div v-for="line in logs" :key="line.id" :class="streamClass(line.stream)">
              <span v-if="line.stream !== 'stdout'" class="text-[10px] text-muted-foreground mr-1">
                [{{ line.stream }}]
              </span>
              {{ line.line }}
            </div>
          </div>
          <p v-else class="text-sm text-muted-foreground">
            {{ t('repo.workflows.job.logsEmpty') }}
          </p>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
