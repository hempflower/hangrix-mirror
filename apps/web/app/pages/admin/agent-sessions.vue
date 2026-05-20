<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { AlertTriangle, ExternalLink, Search } from 'lucide-vue-next'

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

import type { AdminAgentSession, AdminAgentSessionListResp } from '~/types/agent-session'

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

onMounted(load)
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
              <TableHead>{{ t('admin.agentSessions.cols.error') }}</TableHead>
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
              </TableRow>
              <TableRow v-if="r.error_message" class="border-t-0">
                <TableCell />
                <TableCell colspan="8" class="pt-0 pb-3">
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
