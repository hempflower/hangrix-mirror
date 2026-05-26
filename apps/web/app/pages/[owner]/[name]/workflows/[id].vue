<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Check, ChevronDown, ChevronRight, Clock, Code, Copy, Play, ScrollText, Terminal } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import ActorBadge from '@/components/ActorBadge.vue'
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
    detail.value = await $fetch<WorkflowRunDetail>(`/api/repos/${owner.value}/${name.value}/workflow-runs/${id.value}`, {
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
    const res = await $fetch<WorkflowJobLogsResp>(`/api/repos/${owner.value}/${name.value}/workflow-runs/${id.value}/jobs/${jobId}/logs`, {
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

// --- Outputs ---
const expandedSteps = ref<Record<string, boolean>>({})
const copiedKey = ref<string | null>(null)
const expandedScripts = ref<Record<string, boolean>>({})

function hasSteps(job: WorkflowJobRun): boolean {
  return !!(job.steps && job.steps.length > 0)
}

function toggleScript(stepKey: string) {
  expandedScripts.value = {
    ...expandedScripts.value,
    [stepKey]: !expandedScripts.value[stepKey],
  }
}

function stepTypeVariant(type: string) {
  switch (type) {
    case 'release': return 'default' as const
    case 'script': return 'outline' as const
    default: return 'secondary' as const
  }
}

function stepTypeLabel(type: string): string {
  if (type === 'run' || type === '') return t('repo.workflows.stepType.run')
  if (type === 'release') return t('repo.workflows.stepType.release')
  if (type === 'script') return t('repo.workflows.stepType.script')
  return type || 'run'
}

function stepIdKey(step: { id?: string; name: string }): string {
  return step.id || step.name
}

function hasOutputs(job: WorkflowJobRun): boolean {
  return !!(job.job_outputs && Object.keys(job.job_outputs).length > 0)
}

function hasStepOutputs(job: WorkflowJobRun): boolean {
  if (!job.step_outputs) return false
  return Object.values(job.step_outputs).some(v => v && Object.keys(v).length > 0)
}

function toggleStep(stepId: string) {
  expandedSteps.value = {
    ...expandedSteps.value,
    [stepId]: !expandedSteps.value[stepId],
  }
}

async function copyToClipboard(value: string, id: string) {
  try {
    await navigator.clipboard.writeText(value)
    copiedKey.value = id
    setTimeout(() => {
      if (copiedKey.value === id) copiedKey.value = null
    }, 2000)
  } catch {
    // ignore
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
              <template v-if="detail.run.trigger_actor">
                <span>·</span>
                <span class="inline-flex items-center gap-1">
                  {{ t('repo.workflows.triggeredBy') }}:
                  <ActorBadge :actor="detail.run.trigger_actor" size="sm" />
                </span>
              </template>
              <template v-if="detail.run.started_at">
                <span>·</span>
                <span>{{ duration(detail.run.started_at, detail.run.finished_at) }}</span>
              </template>
            </p>
          </div>

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
              class="divide-y"
            >
              <!-- Job row -->
              <div class="flex flex-wrap items-center gap-3 px-3 py-2.5">
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

              <!-- Job outputs -->
              <div v-if="hasOutputs(job)" class="px-3 py-2.5 bg-muted/30">
                <p class="text-xs font-medium text-muted-foreground mb-2">
                  {{ t('repo.workflows.job.outputs') }}
                </p>
                <dl class="space-y-1.5">
                  <div
                    v-for="(val, key) in job.job_outputs"
                    :key="key"
                    class="flex items-start gap-2"
                  >
                    <dt class="text-xs font-mono font-medium min-w-0 shrink-0">
                      {{ key }}
                    </dt>
                    <dd class="text-xs font-mono break-all min-w-0 flex-1 flex items-center gap-1.5">
                      <template v-if="val.masked">
                        <span class="text-muted-foreground italic">***</span>
                        <Badge variant="outline" class="text-[10px] h-4 px-1">{{ t('repo.workflows.job.masked') }}</Badge>
                      </template>
                      <template v-else>
                        <span>{{ val.value }}</span>
                        <button
                          type="button"
                          class="inline-flex items-center text-muted-foreground hover:text-foreground transition-colors"
                          :title="t('repo.workflows.job.copyValue')"
                          @click="copyToClipboard(val.value, `job-${job.id}-${key}`)"
                        >
                          <Check v-if="copiedKey === `job-${job.id}-${key}`" class="size-3" />
                          <Copy v-else class="size-3" />
                        </button>
                      </template>
                    </dd>
                  </div>
                </dl>
              </div>

              <!-- Step list with type badges and script content -->
              <div v-if="hasSteps(job)" class="px-3 py-2.5 bg-muted/30">
                <div class="space-y-2">
                  <div
                    v-for="step in job.steps"
                    :key="stepIdKey(step)"
                  >
                    <div class="flex flex-wrap items-center gap-2">
                      <span class="text-xs font-mono font-medium">{{ step.name }}</span>
                      <Badge :variant="stepTypeVariant(step.type)" class="text-[10px] h-4 px-1">
                        {{ stepTypeLabel(step.type) }}
                      </Badge>
                      <button
                        v-if="step.type === 'script' && step.script"
                        type="button"
                        class="inline-flex items-center gap-1 text-[10px] text-muted-foreground hover:text-foreground transition-colors"
                        @click="toggleScript(`${job.id}-${stepIdKey(step)}`)"
                      >
                        <Code class="size-3" />
                        <template v-if="expandedScripts[`${job.id}-${stepIdKey(step)}`]">
                          {{ t('repo.workflows.step.hideScript') }}
                        </template>
                        <template v-else>
                          {{ t('repo.workflows.step.showScript') }}
                        </template>
                      </button>
                    </div>
                    <pre
                      v-if="step.type === 'script' && step.script && expandedScripts[`${job.id}-${stepIdKey(step)}`]"
                      class="mt-1.5 rounded bg-muted p-2 font-mono text-[11px] leading-relaxed overflow-x-auto whitespace-pre-wrap break-all"
                    >{{ step.script }}</pre>
                  </div>
                </div>
              </div>

              <!-- Step outputs (collapsible) -->
              <div v-if="hasStepOutputs(job)" class="px-3 py-2.5 bg-muted/30">
                <button
                  type="button"
                  class="flex items-center gap-1.5 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors w-full text-left"
                  @click="toggleStep(`job-${job.id}`)"
                >
                  <ChevronDown v-if="expandedSteps[`job-${job.id}`]" class="size-3.5" />
                  <ChevronRight v-else class="size-3.5" />
                  {{ t('repo.workflows.job.stepOutputs') }}
                </button>

                <div v-if="expandedSteps[`job-${job.id}`]" class="mt-2 space-y-3">
                  <div
                    v-for="(outputs, stepId) in job.step_outputs"
                    v-show="outputs && Object.keys(outputs).length > 0"
                    :key="stepId"
                    class="pl-4 border-l-2"
                  >
                    <p class="text-xs font-mono font-medium mb-1.5">{{ stepId }}</p>
                    <div class="space-y-1">
                      <div
                        v-for="(val, key) in outputs"
                        :key="key"
                        class="flex items-start gap-2"
                      >
                        <dt class="text-xs font-mono text-muted-foreground min-w-0 shrink-0">
                          {{ key }}
                        </dt>
                        <dd class="text-xs font-mono break-all min-w-0 flex-1 flex items-center gap-1.5">
                          <template v-if="val.masked">
                            <span class="text-muted-foreground italic">***</span>
                            <Badge variant="outline" class="text-[10px] h-4 px-1">{{ t('repo.workflows.job.masked') }}</Badge>
                          </template>
                          <template v-else>
                            <span>{{ val.value }}</span>
                            <button
                              type="button"
                              class="inline-flex items-center text-muted-foreground hover:text-foreground transition-colors"
                              :title="t('repo.workflows.job.copyValue')"
                              @click="copyToClipboard(val.value, `step-${job.id}-${stepId}-${key}`)"
                            >
                              <Check v-if="copiedKey === `step-${job.id}-${stepId}-${key}`" class="size-3" />
                              <Copy v-else class="size-3" />
                            </button>
                          </template>
                        </dd>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
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
