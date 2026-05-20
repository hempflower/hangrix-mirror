<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Activity, Cpu, Hash, Timer } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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

import type { LLMUsage, LLMUsageDetail, LLMUsageListResp } from '~/types/llm-usage'
import type { LLMProvider, LLMProviderListResp } from '~/types/llm-provider'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()
useHead({ title: () => `${t('admin.usage.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/llm' },
  { label: t('admin.usage.title') },
])

const rows = ref<LLMUsage[]>([])
const total = ref(0)
const providers = ref<LLMProvider[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

// --- detail dialog ---
const detailOpen = ref(false)
const detailLoading = ref(false)
const detailError = ref<string | null>(null)
const detail = ref<LLMUsageDetail | null>(null)

const ANY_PROVIDER = '__any__'
const filterProvider = ref<string>(ANY_PROVIDER)
const filterSince = ref<string>('')
const pageSize = ref<number>(50)
const offset = ref<number>(0)

// Aggregate cards reflect the visible page — total_calls comes from the
// server-side count so it stays accurate even when only one page is loaded.
const totalTokens = computed(() => rows.value.reduce((a, r) => a + r.total_tokens, 0))
const totalCalls = computed(() => total.value)
const errorCalls = computed(() => rows.value.filter(r => r.status_code >= 400 || !!r.error_message).length)
const avgLatency = computed(() => {
  if (rows.value.length === 0) return 0
  const sum = rows.value.reduce((a, r) => a + r.latency_ms, 0)
  return Math.round(sum / rows.value.length)
})

function statusVariant(code: number, errMsg?: string) {
  if (errMsg) return 'destructive'
  if (code >= 400) return 'destructive'
  if (code >= 200 && code < 300) return 'secondary'
  return 'outline'
}

function formatDate(s: string) {
  try { return new Date(s).toLocaleString() } catch { return s }
}

async function loadProviders() {
  try {
    const res = await $fetch<LLMProviderListResp>('/api/admin/llm/providers', { credentials: 'include' })
    providers.value = res.items ?? []
  } catch { /* non-fatal — filter just shows blank */ }
}

async function load() {
  loading.value = true
  error.value = null
  const params: Record<string, string> = {
    limit: String(pageSize.value),
    offset: String(offset.value),
  }
  if (filterProvider.value && filterProvider.value !== ANY_PROVIDER) params.provider = filterProvider.value
  if (filterSince.value) {
    const d = new Date(filterSince.value)
    if (!Number.isNaN(d.getTime())) params.since = d.toISOString()
  }
  try {
    const res = await $fetch<LLMUsageListResp>('/api/admin/llm/usage', {
      credentials: 'include',
      params,
    })
    rows.value = res.items ?? []
    total.value = res.total ?? 0
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.usage.loadFailed')
  } finally {
    loading.value = false
  }
}

async function openDetail(id: number) {
  detailOpen.value = true
  detailLoading.value = true
  detailError.value = null
  detail.value = null
  try {
    const res = await $fetch<LLMUsageDetail>(`/api/admin/llm/usage/${id}`, { credentials: 'include' })
    detail.value = res
  } catch (e: any) {
    detailError.value = e?.data?.error ?? t('admin.usage.detail.loadFailed')
  } finally {
    detailLoading.value = false
  }
}

function formatBody(raw: string | undefined | null): string {
  if (!raw) return ''
  try {
    const parsed = JSON.parse(raw)
    return JSON.stringify(parsed, null, 2)
  } catch {
    return raw
  }
}

// Re-filtering resets pagination — page 3 of "all" rarely lines up with
// page 3 of the new filter set.
function applyFilters() {
  offset.value = 0
  load()
}

function onOffsetChange(v: number) {
  offset.value = v
  load()
}

onMounted(async () => {
  await loadProviders()
  await load()
})
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t('admin.usage.title') }}</h1>
      <p class="text-sm text-muted-foreground">{{ t('admin.usage.subtitle') }}</p>
    </header>

    <!-- Aggregate cards -->
    <div class="grid gap-4 md:grid-cols-4">
      <Card>
        <CardHeader class="pb-2">
          <CardDescription class="flex items-center gap-1.5">
            <Activity class="size-3" />
            {{ t('admin.usage.totalCalls') }}
          </CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ totalCalls }}</CardTitle>
        </CardHeader>
      </Card>
      <Card>
        <CardHeader class="pb-2">
          <CardDescription class="flex items-center gap-1.5">
            <Hash class="size-3" />
            {{ t('admin.usage.totalTokens') }}
          </CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ totalTokens.toLocaleString() }}</CardTitle>
        </CardHeader>
      </Card>
      <Card>
        <CardHeader class="pb-2">
          <CardDescription class="flex items-center gap-1.5">
            <Timer class="size-3" />
            {{ t('admin.usage.avgLatency') }}
          </CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ avgLatency }} ms</CardTitle>
        </CardHeader>
      </Card>
      <Card>
        <CardHeader class="pb-2">
          <CardDescription class="flex items-center gap-1.5">
            <Cpu class="size-3" />
            {{ t('admin.usage.errors') }}
          </CardDescription>
          <CardTitle class="text-3xl tabular-nums">{{ errorCalls }}</CardTitle>
        </CardHeader>
      </Card>
    </div>

    <!-- Filters + table -->
    <Card>
      <CardHeader>
        <CardTitle>{{ t('admin.usage.cardTitle') }}</CardTitle>
        <CardDescription>{{ t('admin.usage.cardDescription') }}</CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="grid gap-3 sm:grid-cols-4">
          <div>
            <Label class="text-xs">{{ t('admin.usage.filters.provider') }}</Label>
            <Select v-model="filterProvider">
              <SelectTrigger>
                <SelectValue :placeholder="t('admin.usage.filters.providerAny')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ANY_PROVIDER">{{ t('admin.usage.filters.providerAny') }}</SelectItem>
                <SelectItem v-for="p in providers" :key="p.id" :value="p.name">{{ p.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <Label class="text-xs">{{ t('admin.usage.filters.since') }}</Label>
            <Input v-model="filterSince" type="datetime-local" />
          </div>
          <div>
            <Label class="text-xs">{{ t('common.pagination.pageSize') }}</Label>
            <Input v-model.number="pageSize" type="number" min="1" max="500" />
          </div>
          <div class="flex items-end">
            <Button class="w-full" @click="applyFilters">{{ t('admin.usage.applyFilters') }}</Button>
          </div>
        </div>

        <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{{ t('admin.usage.cols.time') }}</TableHead>
              <TableHead>{{ t('admin.usage.cols.provider') }}</TableHead>
              <TableHead>{{ t('admin.usage.cols.model') }}</TableHead>
              <TableHead class="text-right">{{ t('admin.usage.cols.prompt') }}</TableHead>
              <TableHead class="text-right">{{ t('admin.usage.cols.completion') }}</TableHead>
              <TableHead class="text-right">{{ t('admin.usage.cols.total') }}</TableHead>
              <TableHead class="text-right">{{ t('admin.usage.cols.latency') }}</TableHead>
              <TableHead>{{ t('admin.usage.cols.status') }}</TableHead>
              <TableHead>{{ t('admin.usage.cols.session') }}</TableHead>
              <TableHead>{{ t('common.actions') }}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="r in rows" :key="r.id">
              <TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{ formatDate(r.created_at) }}</TableCell>
              <TableCell>{{ r.provider_name }}</TableCell>
              <TableCell class="font-mono text-xs">{{ r.model }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ r.prompt_tokens }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ r.completion_tokens }}</TableCell>
              <TableCell class="text-right font-medium tabular-nums">{{ r.total_tokens }}</TableCell>
              <TableCell class="text-right tabular-nums">{{ r.latency_ms }}</TableCell>
              <TableCell>
                <Badge :variant="statusVariant(r.status_code, r.error_message)">
                  {{ r.error_message ? r.error_message : r.status_code }}
                </Badge>
              </TableCell>
              <TableCell class="text-xs text-muted-foreground">
                <code v-if="r.session_id" class="font-mono">#{{ r.session_id }}</code>
                <span v-else>—</span>
              </TableCell>
              <TableCell>
                <Button variant="outline" size="sm" @click="openDetail(r.id)">
                  {{ t('admin.usage.detail.viewDetail') }}
                </Button>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
        <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
        <p v-else-if="rows.length === 0" class="rounded-lg border border-dashed p-8 text-center text-sm text-muted-foreground">
          {{ t('admin.usage.empty') }}
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
    <!-- Detail dialog -->
    <Dialog v-model:open="detailOpen">
      <DialogContent class="max-w-3xl">
        <DialogHeader>
          <DialogTitle>{{ t('admin.usage.detail.title') }}</DialogTitle>
          <DialogDescription v-if="detail">
            <span class="mr-3">{{ t('admin.usage.detail.provider') }}: <strong>{{ detail.provider_name }}</strong></span>
            <span class="mr-3">{{ t('admin.usage.detail.model') }}: <strong>{{ detail.model }}</strong></span>
            <span class="mr-3">{{ t('admin.usage.detail.time') }}: <strong>{{ formatDate(detail.created_at) }}</strong></span>
            <span>{{ t('admin.usage.detail.status') }}: <strong>{{ detail.status_code }}</strong></span>
          </DialogDescription>
        </DialogHeader>

        <div v-if="detailLoading" class="py-12 text-center text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </div>

        <div v-else-if="detailError" class="py-12 text-center text-sm text-destructive">
          {{ detailError }}
        </div>

        <div v-else-if="detail" class="space-y-6">
          <div>
            <h3 class="mb-2 text-sm font-semibold">{{ t('admin.usage.detail.request') }}</h3>
            <pre
              v-if="formatBody(detail.request_body)"
              class="max-h-80 overflow-auto whitespace-pre-wrap rounded bg-muted/40 p-3 font-mono text-xs"
            >{{ formatBody(detail.request_body) }}</pre>
            <p v-else class="text-sm text-muted-foreground">{{ t('admin.usage.detail.emptyBody') }}</p>
          </div>
          <div>
            <h3 class="mb-2 text-sm font-semibold">{{ t('admin.usage.detail.response') }}</h3>
            <pre
              v-if="formatBody(detail.response_body)"
              class="max-h-80 overflow-auto whitespace-pre-wrap rounded bg-muted/40 p-3 font-mono text-xs"
            >{{ formatBody(detail.response_body) }}</pre>
            <p v-else class="text-sm text-muted-foreground">{{ t('admin.usage.detail.emptyBody') }}</p>
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" @click="detailOpen = false">{{ t('common.close') }}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
