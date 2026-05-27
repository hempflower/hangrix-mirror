<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { AlertTriangle, ExternalLink, MoreHorizontal, Search, StopCircle, Trash2 } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Pagination } from '@/components/ui/pagination'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Separator } from '@/components/ui/separator'

import type { AdminAgentSession, AdminAgentSessionListResp, ContainerState } from '~/types/agent-session'
import { useLifecycleSettings } from '~/composables/useLifecycleSettings'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()
useHead({ title: () => `${t('admin.agentSessions.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/agent-sessions' },
  { label: t('admin.agentSessions.title') },
])

const rows = ref<AdminAgentSession[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

const ANY_STATUS = '__any__'
const filterRole = ref<string>('')
const filterStatus = ref<string>(ANY_STATUS)
const filterRepoID = ref<string>('')
const filterSince = ref<string>('')
const pageSize = ref<number>(50)
const offset = ref<number>(0)

const STATUSES = ['pending', 'claimed', 'running', 'idle', 'succeeded', 'failed', 'cancelled', 'archived'] as const

function statusVariant(s: string) {
  if (s === 'running' || s === 'claimed') return 'secondary'
  if (s === 'failed' || s === 'cancelled') return 'destructive'
  if (s === 'archived' || s === 'succeeded') return 'outline'
  return 'outline'
}

function formatDate(s: string) {
  try { return new Date(s).toLocaleString() } catch { return s }
}

function duration(start: string, end?: string | null) {
  const a = new Date(start).getTime()
  const b = end ? new Date(end).getTime() : Date.now()
  if (Number.isNaN(a) || Number.isNaN(b)) return ''
  const sec = Math.max(0, Math.floor((b - a) / 1000))
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

// Summary cards count rows on the visible page; the platform-wide total is
// surfaced by the pager beneath the table.
const liveCount = computed(() => rows.value.filter(r => ['pending', 'claimed', 'running', 'idle'].includes(r.status)).length)
const failedCount = computed(() => rows.value.filter(r => r.status === 'failed').length)

async function load() {
  loading.value = true
  error.value = null
  const params: Record<string, string> = {
    limit: String(pageSize.value),
    offset: String(offset.value),
  }
  if (filterRole.value) params.role_key = filterRole.value
  if (filterStatus.value && filterStatus.value !== ANY_STATUS) params.status = filterStatus.value
  if (filterRepoID.value) params.repo_id = filterRepoID.value
  if (filterSince.value) {
    const d = new Date(filterSince.value)
    if (!Number.isNaN(d.getTime())) params.since = d.toISOString()
  }
  try {
    const res = await $fetch<AdminAgentSessionListResp>('/api/admin/agent-sessions', {
      credentials: 'include',
      params,
    })
    rows.value = res.items ?? []
    total.value = res.total ?? 0
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.agentSessions.loadFailed')
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  offset.value = 0
  load()
}

function onOffsetChange(v: number) {
  offset.value = v
  load()
}

// ---- Lifecycle settings ----

const lifecycle = useLifecycleSettings()
const lifecycleOpen = ref(false)

const SETTING_KEYS = {
  idleStop: 'lifecycle.idle_stop_threshold',
  idleRemoval: 'lifecycle.idle_removal_threshold',
  abandonedCleanup: 'lifecycle.abandoned_cleanup_threshold',
} as const

const idleStopDraft = ref('')
const idleRemovalDraft = ref('')
const abandonedCleanupDraft = ref('')
const idleStopSaving = ref(false)
const idleRemovalSaving = ref(false)
const abandonedCleanupSaving = ref(false)

function syncDrafts() {
  idleStopDraft.value = lifecycle.getValue(SETTING_KEYS.idleStop)
  idleRemovalDraft.value = lifecycle.getValue(SETTING_KEYS.idleRemoval)
  abandonedCleanupDraft.value = lifecycle.getValue(SETTING_KEYS.abandonedCleanup)
}

async function saveSettingKey(key: string, value: string) {
  if (!lifecycle.validateDuration(value)) return
  const ok = await lifecycle.patch(key, value)
  if (ok) syncDrafts()
}

async function saveIdleStop() {
  if (!lifecycle.validateDuration(idleStopDraft.value)) return
  idleStopSaving.value = true
  try { await saveSettingKey(SETTING_KEYS.idleStop, idleStopDraft.value) }
  finally { idleStopSaving.value = false }
}

async function saveIdleRemoval() {
  if (!lifecycle.validateDuration(idleRemovalDraft.value)) return
  idleRemovalSaving.value = true
  try { await saveSettingKey(SETTING_KEYS.idleRemoval, idleRemovalDraft.value) }
  finally { idleRemovalSaving.value = false }
}

async function saveAbandonedCleanup() {
  if (!lifecycle.validateDuration(abandonedCleanupDraft.value)) return
  abandonedCleanupSaving.value = true
  try { await saveSettingKey(SETTING_KEYS.abandonedCleanup, abandonedCleanupDraft.value) }
  finally { abandonedCleanupSaving.value = false }
}

function parseDurationSeconds(dur: string): number {
  if (!dur || dur === '0') return 0
  const m = dur.match(/^(\d+)(s|m|h|d)$/)
  if (!m) return 0
  const n = Number.parseInt(m[1]!, 10)
  switch (m[2]!) {
    case 's': return n
    case 'm': return n * 60
    case 'h': return n * 3600
    case 'd': return n * 86400
    default: return 0
  }
}

// ---- Container state helpers ----

function containerStateVariant(s: ContainerState | undefined | null) {
  if (s === 'running') return 'secondary'
  if (s === 'stopped') return 'outline'
  if (s === 'pending_stop') return 'outline'
  if (s === 'pending_removal') return 'destructive'
  return undefined
}

function containerStateLabel(s: ContainerState | undefined | null): string {
  switch (s) {
    case 'running': return t('admin.agentSessions.container.running')
    case 'stopped': return t('admin.agentSessions.container.stopped')
    case 'pending_stop': return t('admin.agentSessions.container.pendingStop')
    case 'pending_removal': return t('admin.agentSessions.container.pendingRemoval')
    default: return t('admin.agentSessions.container.none')
  }
}

function containerStateClass(s: ContainerState | undefined | null) {
  if (s === 'pending_stop') return 'border-dashed'
  if (s === 'pending_removal') return 'border-dashed'
  return ''
}

function showStopAction(r: AdminAgentSession): boolean {
  return r.container_state === 'running'
}

function showRemoveAction(r: AdminAgentSession): boolean {
  return r.container_state === 'running' || r.container_state === 'stopped'
}

const actionSessionId = ref<number | null>(null)

async function onStopContainer(r: AdminAgentSession) {
  if (actionSessionId.value === r.session_id) return
  actionSessionId.value = r.session_id
  try {
    await $fetch(`/api/admin/agent-sessions/${r.session_id}/stop-container`, {
      method: 'POST',
      credentials: 'include',
    })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.agentSessions.stopFailed')
  } finally {
    actionSessionId.value = null
  }
}

async function onRemoveContainer(r: AdminAgentSession) {
  if (actionSessionId.value === r.session_id) return
  actionSessionId.value = r.session_id
  try {
    await $fetch(`/api/admin/agent-sessions/${r.session_id}/remove-container`, {
      method: 'POST',
      credentials: 'include',
    })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.agentSessions.removeFailed')
  } finally {
    actionSessionId.value = null
  }
}

onMounted(async () => {
  await lifecycle.load()
  syncDrafts()
  load()
})
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t('admin.agentSessions.title') }}</h1>
      <p class="text-sm text-muted-foreground">{{ t('admin.agentSessions.subtitle') }}</p>
    </header>

    <div class="grid gap-4 md:grid-cols-3">
      <Card>
        <CardHeader class="pb-2">
          <CardDescription>{{ t('admin.agentSessions.summary.total') }}</CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ total }}</CardTitle>
        </CardHeader>
      </Card>
      <Card>
        <CardHeader class="pb-2">
          <CardDescription>{{ t('admin.agentSessions.summary.live') }}</CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ liveCount }}</CardTitle>
        </CardHeader>
      </Card>
      <Card>
        <CardHeader class="pb-2">
          <CardDescription>{{ t('admin.agentSessions.summary.failed') }}</CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ failedCount }}</CardTitle>
        </CardHeader>
      </Card>
    </div>

    <!-- Lifecycle settings card -->
    <Collapsible v-model:open="lifecycleOpen" class="overflow-hidden rounded-xl border bg-card text-card-foreground shadow-sm">
      <CollapsibleTrigger as-child>
        <div class="flex cursor-pointer items-center justify-between p-6 hover:bg-accent/50">
          <div class="space-y-1">
            <h3 class="font-semibold leading-none tracking-tight">
              {{ t('admin.agentSessions.lifecycle.title') }}
            </h3>
            <p class="text-sm text-muted-foreground">
              {{ t('admin.agentSessions.lifecycle.subtitle') }}
            </p>
          </div>
          <div class="flex items-center gap-3">
            <p v-if="lifecycle.loading" class="text-xs text-muted-foreground">{{ t('common.loading') }}</p>
            <Button variant="ghost" size="icon" class="size-8 rounded-full" :class="lifecycleOpen ? 'rotate-180' : ''" as="span">
              <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="transition-transform duration-200"><path d="m6 9 6 6 6-6"/></svg>
            </Button>
          </div>
        </div>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <Separator />
        <div class="space-y-4 p-6">
          <!-- Idle stop threshold -->
          <div class="grid gap-1.5 sm:grid-cols-[200px_1fr]">
            <Label class="pt-1.5 text-sm font-medium" for="idle-stop">
              {{ t('admin.agentSessions.lifecycle.idleStop') }}
            </Label>
            <div class="space-y-1.5">
              <div class="flex items-center gap-2">
                <Input
                  id="idle-stop"
                  v-model="idleStopDraft"
                  class="w-28 font-mono"
                  :class="lifecycle.validateDuration(idleStopDraft) ? '' : 'border-destructive'"
                  @blur="saveIdleStop()"
                  @keyup.enter="saveIdleStop()"
                />
                <span v-if="idleStopSaving" class="text-xs text-muted-foreground">{{ t('common.saving') }}</span>
              </div>
              <p class="text-xs text-muted-foreground">
                {{ t('admin.agentSessions.lifecycle.idleStopHint') }}
                {{ t('admin.agentSessions.lifecycle.defaultLabel', { value: lifecycle.getDefaultValue(SETTING_KEYS.idleStop) }) }}
              </p>
              <p v-if="lifecycle.getUpdatedMeta(SETTING_KEYS.idleStop).at" class="text-xs text-muted-foreground">
                {{ t('admin.agentSessions.lifecycle.lastUpdated', {
                  time: formatDate(lifecycle.getUpdatedMeta(SETTING_KEYS.idleStop).at!),
                  by: lifecycle.getUpdatedMeta(SETTING_KEYS.idleStop).by ?? '—',
                }) }}
              </p>
            </div>
          </div>

          <!-- Idle removal threshold -->
          <div class="grid gap-1.5 sm:grid-cols-[200px_1fr]">
            <Label class="pt-1.5 text-sm font-medium" for="idle-removal">
              {{ t('admin.agentSessions.lifecycle.idleRemoval') }}
            </Label>
            <div class="space-y-1.5">
              <div class="flex items-center gap-2">
                <Input
                  id="idle-removal"
                  v-model="idleRemovalDraft"
                  class="w-28 font-mono"
                  :class="lifecycle.validateDuration(idleRemovalDraft) ? '' : 'border-destructive'"
                  @blur="saveIdleRemoval()"
                  @keyup.enter="saveIdleRemoval()"
                />
                <span v-if="idleRemovalSaving" class="text-xs text-muted-foreground">{{ t('common.saving') }}</span>
              </div>
              <p class="text-xs text-muted-foreground">
                {{ t('admin.agentSessions.lifecycle.idleRemovalHint') }}
                {{ t('admin.agentSessions.lifecycle.defaultLabel', { value: lifecycle.getDefaultValue(SETTING_KEYS.idleRemoval) }) }}
              </p>
              <p v-if="lifecycle.getUpdatedMeta(SETTING_KEYS.idleRemoval).at" class="text-xs text-muted-foreground">
                {{ t('admin.agentSessions.lifecycle.lastUpdated', {
                  time: formatDate(lifecycle.getUpdatedMeta(SETTING_KEYS.idleRemoval).at!),
                  by: lifecycle.getUpdatedMeta(SETTING_KEYS.idleRemoval).by ?? '—',
                }) }}
              </p>
            </div>
          </div>

          <!-- Abandoned cleanup threshold -->
          <div class="grid gap-1.5 sm:grid-cols-[200px_1fr]">
            <Label class="pt-1.5 text-sm font-medium" for="abandoned-cleanup">
              {{ t('admin.agentSessions.lifecycle.abandonedCleanup') }}
            </Label>
            <div class="space-y-1.5">
              <div class="flex items-center gap-2">
                <Input
                  id="abandoned-cleanup"
                  v-model="abandonedCleanupDraft"
                  class="w-28 font-mono"
                  :class="lifecycle.validateDuration(abandonedCleanupDraft) ? '' : 'border-destructive'"
                  @blur="saveAbandonedCleanup()"
                  @keyup.enter="saveAbandonedCleanup()"
                />
                <span v-if="abandonedCleanupSaving" class="text-xs text-muted-foreground">{{ t('common.saving') }}</span>
              </div>
              <p class="text-xs text-muted-foreground">
                {{ t('admin.agentSessions.lifecycle.abandonedCleanupHint') }}
                {{ t('admin.agentSessions.lifecycle.defaultLabel', { value: lifecycle.getDefaultValue(SETTING_KEYS.abandonedCleanup) }) }}
              </p>
              <p v-if="lifecycle.getUpdatedMeta(SETTING_KEYS.abandonedCleanup).at" class="text-xs text-muted-foreground">
                {{ t('admin.agentSessions.lifecycle.lastUpdated', {
                  time: formatDate(lifecycle.getUpdatedMeta(SETTING_KEYS.abandonedCleanup).at!),
                  by: lifecycle.getUpdatedMeta(SETTING_KEYS.abandonedCleanup).by ?? '—',
                }) }}
              </p>
            </div>
          </div>
        </div>
      </CollapsibleContent>
    </Collapsible>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('admin.agentSessions.cardTitle') }}</CardTitle>
        <CardDescription>{{ t('admin.agentSessions.cardDescription') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="grid gap-3 sm:grid-cols-3 lg:grid-cols-6">
          <div>
            <Label class="text-xs">{{ t('admin.agentSessions.filters.role') }}</Label>
            <Input v-model="filterRole" placeholder="backend" />
          </div>
          <div>
            <Label class="text-xs">{{ t('admin.agentSessions.filters.status') }}</Label>
            <Select v-model="filterStatus">
              <SelectTrigger>
                <SelectValue :placeholder="t('admin.agentSessions.filters.statusAny')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ANY_STATUS">{{ t('admin.agentSessions.filters.statusAny') }}</SelectItem>
                <SelectItem v-for="s in STATUSES" :key="s" :value="s">{{ s }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <Label class="text-xs">{{ t('admin.agentSessions.filters.repoId') }}</Label>
            <Input v-model="filterRepoID" type="number" placeholder="42" />
          </div>
          <div>
            <Label class="text-xs">{{ t('admin.agentSessions.filters.since') }}</Label>
            <Input v-model="filterSince" type="datetime-local" />
          </div>
          <div>
            <Label class="text-xs">{{ t('common.pagination.pageSize') }}</Label>
            <Input v-model.number="pageSize" type="number" min="1" max="500" />
          </div>
          <div class="flex items-end">
            <Button class="w-full" @click="applyFilters">
              <Search class="size-4" />
              {{ t('admin.agentSessions.applyFilters') }}
            </Button>
          </div>
        </div>

        <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('admin.agentSessions.cols.id') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.role') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.status') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.container') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.cause') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.repoIssue') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.repoSha') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.created') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.duration') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.error') }}</TableHead>
              <TableHead class="w-12" />
            </TableRow>
          </TableHeader>
          <TableBody>
            <template v-for="r in rows" :key="r.session_id">
              <TableRow>
                <TableCell class="font-mono text-xs">#{{ r.session_id }}</TableCell>
                <TableCell class="font-medium">{{ r.role_key }}</TableCell>
                <TableCell><Badge :variant="statusVariant(r.status)">{{ r.status }}</Badge></TableCell>
                <TableCell>
                  <Badge
                    v-if="r.container_state && r.container_state !== 'none'"
                    :variant="containerStateVariant(r.container_state)"
                    :class="containerStateClass(r.container_state)"
                  >
                    {{ containerStateLabel(r.container_state) }}
                  </Badge>
                  <span v-else class="text-xs text-muted-foreground">—</span>
                </TableCell>
                <TableCell class="text-xs text-muted-foreground">
                  {{ r.cause_kind }}<span v-if="r.cause_id"> · {{ r.cause_id }}</span>
                </TableCell>
                <TableCell class="text-xs text-muted-foreground">
                  <code class="font-mono">repo#{{ r.repo_id }} / issue#{{ r.issue_number }}</code>
                </TableCell>
                <TableCell><code class="font-mono text-xs text-muted-foreground">{{ r.repo_sha.slice(0, 12) }}</code></TableCell>
                <TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{ formatDate(r.created_at) }}</TableCell>
                <TableCell class="text-xs text-muted-foreground">{{ duration(r.created_at, r.ended_at) }}</TableCell>
                <TableCell>
                  <span
                    v-if="r.error_message || (r.exit_code != null && r.exit_code !== 0)"
                    class="flex items-center gap-1 text-xs text-destructive"
                  >
                    <AlertTriangle class="size-3" />
                    <span v-if="r.exit_code != null">exit {{ r.exit_code }}</span>
                    <span v-else>error</span>
                  </span>
                  <span v-else class="text-xs text-muted-foreground">—</span>
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger as-child>
                      <Button
                        variant="ghost"
                        size="icon"
                        class="size-8"
                        :disabled="actionSessionId === r.session_id"
                      >
                        <MoreHorizontal class="size-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem
                        v-if="showStopAction(r)"
                        @click="onStopContainer(r)"
                      >
                        <StopCircle class="size-4" />
                        {{ t('admin.agentSessions.actions.stopContainer') }}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        v-if="showRemoveAction(r)"
                        @click="onRemoveContainer(r)"
                        class="text-destructive"
                      >
                        <Trash2 class="size-4" />
                        {{ t('admin.agentSessions.actions.removeContainer') }}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
              <TableRow v-if="r.error_message" class="border-t-0">
                <TableCell />
                <TableCell colspan="10" class="pt-0 pb-3">
                  <pre class="whitespace-pre-wrap wrap-break-word rounded border border-destructive/40 bg-destructive/5 p-2 font-mono text-[11px] text-destructive">{{ r.error_message }}</pre>
                </TableCell>
              </TableRow>
            </template>
          </TableBody>
        </Table>
        <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
        <p v-else-if="rows.length === 0" class="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {{ t('admin.agentSessions.empty') }}
        </p>
        <p v-if="rows.length > 0" class="text-xs text-muted-foreground">
          {{ t('admin.agentSessions.openHint') }}
          <ExternalLink class="inline size-3" />
        </p>

        <Pagination
          v-if="total > 0"
          :total="total"
          :offset="offset"
          :limit="pageSize"
          @update:offset="onOffsetChange"
        />
      </CardContent>
    </Card>
  </div>
</template>
