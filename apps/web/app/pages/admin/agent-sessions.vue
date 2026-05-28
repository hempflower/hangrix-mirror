<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { AlertTriangle, ChevronDown, ExternalLink, MoreHorizontal, Search, StopCircle, Trash2 } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
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

import { useLifecycleSettings } from '~/composables/useLifecycleSettings'
import type { AdminAgentSession, AdminAgentSessionListResp, ContainerState } from '~/types/agent-session'
import { deriveContainerState } from '~/types/agent-session'
import { LIFECYCLE_KEYS } from '~/types/platform-settings'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()
useHead({ title: () => `${t('admin.agentSessions.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/agent-sessions' },
  { label: t('admin.agentSessions.title') },
])

// ── Lifecycle settings ────────────────────────────────────────────────
const { settings: lifecycleItems, loading: lifecycleLoading, load: loadLifecycle, patchSetting } = useLifecycleSettings()
const lifecycleOpen = ref(false)
const lifecyclePatchError = ref<string | null>(null)

/** Look up a known setting value by key. */
function lifecycleValue(key: string): string {
  return lifecycleItems.value.find(s => s.key === key)?.value ?? ''
}

async function onLifecycleBlur(key: string, value: string) {
  lifecyclePatchError.value = null
  try {
    await patchSetting(key, value)
  } catch {
    lifecyclePatchError.value = t('admin.lifecycle.patchFailed')
  }
}

// ── Session list ──────────────────────────────────────────────────────
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

function containerStateVariant(s: ContainerState) {
  if (s === 'running') return 'secondary' as const
  if (s === 'pending_stop' || s === 'stopped') return 'outline' as const
  if (s === 'pending_removal') return 'outline' as const
  return undefined
}

function containerStateClass(s: ContainerState): string | undefined {
  if (s === 'pending_stop') return 'border-dashed'
  if (s === 'pending_removal') return 'border-destructive text-destructive'
  return undefined
}

function containerStateLabel(s: ContainerState): string {
  const keyMap: Record<ContainerState, string> = {
    running: 'admin.lifecycle.containerState.running',
    stopped: 'admin.lifecycle.containerState.stopped',
    pending_stop: 'admin.lifecycle.containerState.pendingStop',
    pending_removal: 'admin.lifecycle.containerState.pendingRemoval',
    none: 'admin.lifecycle.containerState.none',
  }
  return t(keyMap[s])
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

// ── Container actions ─────────────────────────────────────────────────
async function stopContainer(row: AdminAgentSession) {
  if (!confirm(t('admin.lifecycle.actions.stopConfirm', { id: row.session_id }))) return
  try {
    await $fetch(`/api/admin/agent-sessions/${row.session_id}/stop-container`, {
      method: 'POST',
      credentials: 'include',
    })
    row.container_stop_pending = true
  } catch (e: any) {
    alert(e?.data?.error ?? t('admin.lifecycle.actions.stopFailed'))
  }
}

async function removeContainer(row: AdminAgentSession) {
  if (!confirm(t('admin.lifecycle.actions.removeConfirm', { id: row.session_id }))) return
  try {
    await $fetch(`/api/admin/agent-sessions/${row.session_id}/remove-container`, {
      method: 'POST',
      credentials: 'include',
    })
    row.container_cleanup_pending = true
  } catch (e: any) {
    alert(e?.data?.error ?? t('admin.lifecycle.actions.removeFailed'))
  }
}

onMounted(() => {
  loadLifecycle()
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

    <!-- ── Lifecycle settings card ──────────────────────────────── -->
    <Collapsible v-model:open="lifecycleOpen">
      <Card>
        <CardHeader class="pb-3">
          <div class="flex items-center justify-between">
            <div>
              <CardTitle class="text-base">{{ t('admin.lifecycle.title') }}</CardTitle>
              <CardDescription>{{ t('admin.lifecycle.subtitle') }}</CardDescription>
            </div>
            <CollapsibleTrigger as-child>
              <Button variant="ghost" size="sm" class="size-8 p-0">
                <ChevronDown
                  class="size-4 transition-transform duration-200"
                  :class="{ 'rotate-180': lifecycleOpen }"
                />
              </Button>
            </CollapsibleTrigger>
          </div>
        </CardHeader>
        <CollapsibleContent>
          <CardContent class="space-y-4 pt-0">
            <p v-if="lifecycleLoading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
            <template v-else-if="lifecycleItems.length > 0">
              <div class="grid gap-4 sm:grid-cols-3">
                <!-- Idle stop -->
                <div>
                  <Label class="text-xs" for="lifecycle-idle">{{ t('admin.lifecycle.idleStop') }}</Label>
                  <Input
                    id="lifecycle-idle"
                    :model-value="lifecycleValue(LIFECYCLE_KEYS.idleStop)"
                    class="mt-1"
                    @blur="(e: FocusEvent) => {
                      const v = (e.target as HTMLInputElement).value.trim()
                      if (v && v !== lifecycleValue(LIFECYCLE_KEYS.idleStop)) {
                        onLifecycleBlur(LIFECYCLE_KEYS.idleStop, v)
                      }
                    }"
                  />
                  <p class="mt-1 text-[11px] text-muted-foreground">
                    {{ t('admin.lifecycle.idleStopHint') }}
                  </p>
                </div>
                <!-- Archive remove -->
                <div>
                  <Label class="text-xs" for="lifecycle-archive">{{ t('admin.lifecycle.archiveRemove') }}</Label>
                  <Input
                    id="lifecycle-archive"
                    :model-value="lifecycleValue(LIFECYCLE_KEYS.idleRemoval)"
                    class="mt-1"
                    @blur="(e: FocusEvent) => {
                      const v = (e.target as HTMLInputElement).value.trim()
                      if (v && v !== lifecycleValue(LIFECYCLE_KEYS.idleRemoval)) {
                        onLifecycleBlur(LIFECYCLE_KEYS.idleRemoval, v)
                      }
                    }"
                  />
                  <p class="mt-1 text-[11px] text-muted-foreground">
                    {{ t('admin.lifecycle.archiveRemoveHint') }}
                  </p>
                </div>
                <!-- Abandoned cleanup -->
                <div>
                  <Label class="text-xs" for="lifecycle-abandoned">{{ t('admin.lifecycle.periodicCheck') }}</Label>
                  <Input
                    id="lifecycle-abandoned"
                    :model-value="lifecycleValue(LIFECYCLE_KEYS.abandonedCleanup)"
                    class="mt-1"
                    @blur="(e: FocusEvent) => {
                      const v = (e.target as HTMLInputElement).value.trim()
                      if (v && v !== lifecycleValue(LIFECYCLE_KEYS.abandonedCleanup)) {
                        onLifecycleBlur(LIFECYCLE_KEYS.abandonedCleanup, v)
                      }
                    }"
                  />
                  <p class="mt-1 text-[11px] text-muted-foreground">
                    {{ t('admin.lifecycle.periodicCheckHint') }}
                  </p>
                </div>
              </div>
              <p v-if="lifecyclePatchError" class="text-sm text-destructive">{{ lifecyclePatchError }}</p>
              <p class="text-xs text-muted-foreground">{{ t('admin.lifecycle.asyncHint') }}</p>
            </template>
            <p v-else class="text-sm text-muted-foreground">{{ t('admin.lifecycle.loadFailed') }}</p>
          </CardContent>
        </CollapsibleContent>
      </Card>
    </Collapsible>

    <!-- ── Session table ────────────────────────────────────────── -->
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
              <TableHead>{{ t('admin.agentSessions.cols.cause') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.repoIssue') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.repoSha') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.created') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.duration') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.container') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.containerActivity') }}</TableHead>
              <TableHead>{{ t('admin.agentSessions.cols.error') }}</TableHead>
              <TableHead class="w-10">{{ t('admin.agentSessions.cols.actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <template v-for="r in rows" :key="r.session_id">
              <TableRow>
                <TableCell class="font-mono text-xs">#{{ r.session_id }}</TableCell>
                <TableCell class="font-medium">{{ r.role_key }}</TableCell>
                <TableCell><Badge :variant="statusVariant(r.status)">{{ r.status }}</Badge></TableCell>
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
                  <template v-if="deriveContainerState(r) === 'none'">
                    <span class="text-xs text-muted-foreground">{{ containerStateLabel('none') }}</span>
                  </template>
                  <Badge v-else :variant="containerStateVariant(deriveContainerState(r))" :class="containerStateClass(deriveContainerState(r))">
                    {{ containerStateLabel(deriveContainerState(r)) }}
                  </Badge>
                </TableCell>
                <TableCell class="whitespace-nowrap text-xs text-muted-foreground">
                  {{ r.container_last_used_at ? formatDate(r.container_last_used_at) : '—' }}
                </TableCell>
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
                  <DropdownMenu v-if="deriveContainerState(r) === 'running' || deriveContainerState(r) === 'stopped'">
                    <DropdownMenuTrigger as-child>
                      <Button variant="ghost" size="icon" class="size-7">
                        <MoreHorizontal class="size-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem
                        v-if="deriveContainerState(r) === 'running'"
                        @click="stopContainer(r)"
                      >
                        <StopCircle class="size-4" />
                        {{ t('admin.lifecycle.actions.stopContainer') }}
                      </DropdownMenuItem>
                      <DropdownMenuItem
                        @click="removeContainer(r)"
                      >
                        <Trash2 class="size-4" />
                        {{ t('admin.lifecycle.actions.removeContainer') }}
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableCell>
              </TableRow>
              <TableRow v-if="r.error_message" class="border-t-0">
                <TableCell />
                <TableCell colspan="11" class="pt-0 pb-3">
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
