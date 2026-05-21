<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  Activity,
  AlertTriangle,
  BarChart3,
  Calendar,
  Cpu,
  Hash,
  LineChart,
  Server,
  Users,
} from 'lucide-vue-next'
import { Line } from 'vue-chartjs'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler,
  type ChartData,
  type ChartOptions,
} from 'chart.js'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

import type { DashboardResponse, DailyCallsPoint, DailyTokensPoint, RecentFailure } from '~/types/dashboard'
import type { LLMProvider, LLMProviderListResp } from '~/types/llm-provider'

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler,
)

definePageMeta({ layout: 'admin' })

const { t } = useI18n()
useHead({ title: () => `${t('admin.dashboard.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/users' },
  { label: t('admin.dashboard.title') },
])

// --- State --------------------------------------------------------------

const data = ref<DashboardResponse | null>(null)
const providers = ref<LLMProvider[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

const ANY_PROVIDER = '__any__'

const rangePreset = ref<'7d' | '30d' | 'custom'>('7d')
const filterProvider = ref<string>(ANY_PROVIDER)
const customSince = ref('')
const customUntil = ref('')

// --- Derived ------------------------------------------------------------

const rangeLabel = computed(() => {
  if (rangePreset.value === '7d') return t('admin.dashboard.range7d')
  if (rangePreset.value === '30d') return t('admin.dashboard.range30d')
  return t('admin.dashboard.rangeCustom')
})

function buildParams(): Record<string, string> {
  const params: Record<string, string> = {}

  const now = new Date()
  if (rangePreset.value === 'custom') {
    if (customSince.value) {
      const d = new Date(customSince.value)
      if (!Number.isNaN(d.getTime())) params.since = d.toISOString()
    }
    if (customUntil.value) {
      const d = new Date(customUntil.value)
      if (!Number.isNaN(d.getTime())) params.until = d.toISOString()
    }
  } else {
    const days = rangePreset.value === '7d' ? 7 : 30
    const since = new Date(now.getTime() - days * 24 * 60 * 60 * 1000)
    params.since = since.toISOString()
    params.until = now.toISOString()
  }

  if (filterProvider.value && filterProvider.value !== ANY_PROVIDER) {
    params.provider = filterProvider.value
  }

  return params
}

// --- Data loading -------------------------------------------------------

async function loadProviders() {
  try {
    const res = await $fetch<LLMProviderListResp>('/api/admin/llm/providers', { credentials: 'include' })
    providers.value = res.items ?? []
  } catch { /* non-fatal */ }
}

async function load() {
  loading.value = true
  error.value = null
  data.value = null
  try {
    const res = await $fetch<DashboardResponse>('/api/admin/dashboard', {
      credentials: 'include',
      params: buildParams(),
    })
    data.value = res
  } catch (e: any) {
    error.value = e?.data?.error ?? t('admin.dashboard.loadFailed')
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  load()
}

// --- Chart data ---------------------------------------------------------

const callsChartData = computed<ChartData<'line'>>(() => ({
  labels: data.value?.timeseries.daily_calls.map((p: DailyCallsPoint) => p.date) ?? [],
  datasets: [{
    label: t('admin.dashboard.chartCalls'),
    data: data.value?.timeseries.daily_calls.map((p: DailyCallsPoint) => p.count) ?? [],
    borderColor: 'oklch(0.646 0.222 41.116)',
    backgroundColor: 'oklch(0.646 0.222 41.116 / 0.08)',
    fill: true,
    tension: 0.3,
    pointRadius: 3,
    pointHoverRadius: 5,
  }],
}))

const tokensChartData = computed<ChartData<'line'>>(() => {
  const points: DailyTokensPoint[] = data.value?.timeseries.daily_tokens ?? []
  return {
    labels: points.map(p => p.date),
    datasets: [
      {
        label: t('admin.dashboard.chartTotalTokens'),
        data: points.map(p => p.total_tokens),
        borderColor: 'oklch(0.6 0.118 184.704)',
        backgroundColor: 'oklch(0.6 0.118 184.704 / 0.08)',
        fill: true,
        tension: 0.3,
        pointRadius: 3,
        pointHoverRadius: 5,
      },
      {
        label: t('admin.dashboard.chartPromptTokens'),
        data: points.map(p => p.prompt_tokens),
        borderColor: 'oklch(0.398 0.07 227.392)',
        backgroundColor: 'transparent',
        tension: 0.3,
        pointRadius: 2,
        pointHoverRadius: 4,
        borderDash: [4, 3],
      },
      {
        label: t('admin.dashboard.chartCompletionTokens'),
        data: points.map(p => p.completion_tokens),
        borderColor: 'oklch(0.828 0.189 84.429)',
        backgroundColor: 'transparent',
        tension: 0.3,
        pointRadius: 2,
        pointHoverRadius: 4,
        borderDash: [4, 3],
      },
    ],
  }
})

// Chart.js renders on Canvas, which cannot resolve CSS custom properties.
// Use hard-coded oklch() values matching the dark theme (the app always uses dark mode).
const CHART_MUTED = 'oklch(0.708 0 0)'      // --muted-foreground in dark
const CHART_GRID = 'oklch(1 0 0 / 0.06)'    // --border (10%) * 0.6 in dark
const CHART_TOOLTIP_BG = 'oklch(0.205 0 0)' // --popover in dark
const CHART_TOOLTIP_FG = 'oklch(0.985 0 0)' // --popover-foreground in dark
const CHART_TOOLTIP_BORDER = 'oklch(1 0 0 / 0.1)' // --border in dark

const chartOptions: ChartOptions<'line'> = {
responsive: true,
maintainAspectRatio: false,
interaction: {
  mode: 'index',
  intersect: false,
},
plugins: {
  legend: {
  display: true,
  position: 'bottom',
  labels: {
  usePointStyle: true,
  boxWidth: 8,
  padding: 16,
  font: { size: 12 },
  color: CHART_MUTED,
  },
  },
  tooltip: {
  backgroundColor: CHART_TOOLTIP_BG,
  titleColor: CHART_TOOLTIP_FG,
  bodyColor: CHART_TOOLTIP_FG,
  borderColor: CHART_TOOLTIP_BORDER,
  borderWidth: 1,
  },
},
scales: {
  x: {
  grid: { display: false },
  ticks: { font: { size: 11 }, color: CHART_MUTED },
  },
  y: {
  grid: { color: CHART_GRID },
  ticks: { font: { size: 11 }, color: CHART_MUTED },
  beginAtZero: true,
  },
},
}

const tokensChartOptions: ChartOptions<'line'> = {
  ...chartOptions,
  scales: {
    ...chartOptions.scales,
    y: {
      ...(chartOptions.scales as any)?.y,
      ticks: {
        ...((chartOptions.scales as any)?.y?.ticks ?? {}),
        callback: (value: any) => {
          if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
          if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
          return value
        },
      } as any,
    },
  },
}

// --- Helpers ------------------------------------------------------------

function formatNum(n: number | undefined | null): string {
  if (n == null || n === 0) return '0'
  return n.toLocaleString()
}

function formatDate(s: string) {
  try { return new Date(s).toLocaleString() } catch { return s }
}

function tokenLabel(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function failureStatusVariant(_failure: RecentFailure) {
  // Always destructive for failures
  return 'destructive' as const
}

// --- Lifecycle ----------------------------------------------------------

onMounted(async () => {
  await loadProviders()
  await load()
})
</script>

<template>
  <div class="space-y-6">
    <!-- Header -->
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t('admin.dashboard.title') }}</h1>
      <p class="text-sm text-muted-foreground">{{ t('admin.dashboard.subtitle') }}</p>
    </header>

    <!-- Filters -->
    <Card>
      <CardContent class="pt-6">
        <div class="flex flex-wrap items-end gap-4">
          <!-- Range preset -->
          <div class="min-w-0">
            <Label class="text-xs">{{ t('admin.dashboard.filters.range') }}</Label>
            <Select v-model="rangePreset">
              <SelectTrigger class="w-[160px]">
                <SelectValue :placeholder="rangeLabel" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="7d">{{ t('admin.dashboard.range7d') }}</SelectItem>
                <SelectItem value="30d">{{ t('admin.dashboard.range30d') }}</SelectItem>
                <SelectItem value="custom">{{ t('admin.dashboard.rangeCustom') }}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <!-- Custom date range -->
          <template v-if="rangePreset === 'custom'">
            <div class="min-w-0">
              <Label class="text-xs">{{ t('admin.dashboard.filters.since') }}</Label>
              <Input v-model="customSince" type="datetime-local" class="w-[210px]" />
            </div>
            <div class="min-w-0">
              <Label class="text-xs">{{ t('admin.dashboard.filters.until') }}</Label>
              <Input v-model="customUntil" type="datetime-local" class="w-[210px]" />
            </div>
          </template>

          <!-- Provider -->
          <div class="min-w-0">
            <Label class="text-xs">{{ t('admin.dashboard.filters.provider') }}</Label>
            <Select v-model="filterProvider">
              <SelectTrigger class="w-[180px]">
                <SelectValue :placeholder="t('admin.dashboard.filters.providerAny')" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem :value="ANY_PROVIDER">{{ t('admin.dashboard.filters.providerAny') }}</SelectItem>
                <SelectItem v-for="p in providers" :key="p.id" :value="p.name">{{ p.name }}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Button @click="applyFilters">
            {{ t('admin.dashboard.applyFilters') }}
          </Button>
        </div>
      </CardContent>
    </Card>

    <!-- Error -->
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <!-- Loading skeleton -->
    <template v-if="loading">
      <div class="grid gap-4 md:grid-cols-4">
        <Card v-for="i in 4" :key="i">
          <CardHeader class="pb-2">
            <CardDescription class="h-3 w-20 animate-pulse rounded bg-muted" />
            <CardTitle class="h-8 w-16 animate-pulse rounded bg-muted" />
          </CardHeader>
        </Card>
      </div>
      <div class="grid gap-4 lg:grid-cols-2">
        <Card v-for="i in 2" :key="'c' + i">
          <CardHeader>
            <CardTitle class="h-4 w-32 animate-pulse rounded bg-muted" />
          </CardHeader>
          <CardContent>
            <div class="h-[240px] animate-pulse rounded bg-muted" />
          </CardContent>
        </Card>
      </div>
    </template>

    <!-- KPI Cards -->
    <template v-if="data && !loading">
      <div class="grid gap-4 md:grid-cols-4">
        <!-- Total Calls -->
        <Card>
          <CardHeader class="pb-2">
            <CardDescription class="flex items-center gap-1.5">
              <Activity class="size-3" />
              {{ t('admin.dashboard.kpiCalls') }}
            </CardDescription>
            <CardTitle class="text-3xl tabular-nums">{{ formatNum(data.summary.total_calls) }}</CardTitle>
          </CardHeader>
        </Card>

        <!-- Total Tokens -->
        <Card>
          <CardHeader class="pb-2">
            <CardDescription class="flex items-center gap-1.5">
              <Hash class="size-3" />
              {{ t('admin.dashboard.kpiTokens') }}
            </CardDescription>
            <CardTitle class="text-3xl tabular-nums">{{ tokenLabel(data.summary.total_tokens) }}</CardTitle>
          </CardHeader>
        </Card>

        <!-- Active Sessions -->
        <Card>
          <CardHeader class="pb-2">
            <CardDescription class="flex items-center gap-1.5">
              <Users class="size-3" />
              {{ t('admin.dashboard.kpiSessions') }}
            </CardDescription>
            <CardTitle class="text-3xl tabular-nums">{{ formatNum(data.summary.active_sessions) }}</CardTitle>
          </CardHeader>
        </Card>

        <!-- Online Runners -->
        <Card>
          <CardHeader class="pb-2">
            <CardDescription class="flex items-center gap-1.5">
              <Server class="size-3" />
              {{ t('admin.dashboard.kpiRunners') }}
            </CardDescription>
            <CardTitle class="text-3xl tabular-nums">
              {{ formatNum(data.summary.online_runners) }}
              <span class="text-lg font-normal text-muted-foreground">/ {{ formatNum(data.summary.total_runners) }}</span>
            </CardTitle>
          </CardHeader>
        </Card>
      </div>

      <!-- Charts row -->
      <div class="grid gap-4 lg:grid-cols-2">
        <!-- Daily Calls -->
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <LineChart class="size-4 text-chart-1" />
              {{ t('admin.dashboard.chartCallsTitle') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div class="h-[260px]">
              <Line v-if="data.timeseries.daily_calls.length > 0" :data="callsChartData" :options="chartOptions" />
              <div v-else class="flex h-full items-center justify-center rounded-lg border border-dashed text-xs text-muted-foreground">
                {{ t('admin.dashboard.emptyChart') }}
              </div>
            </div>
          </CardContent>
        </Card>

        <!-- Daily Tokens -->
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <BarChart3 class="size-4 text-chart-2" />
              {{ t('admin.dashboard.chartTokensTitle') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div class="h-[260px]">
              <Line v-if="data.timeseries.daily_tokens.length > 0" :data="tokensChartData" :options="tokensChartOptions" />
              <div v-else class="flex h-full items-center justify-center rounded-lg border border-dashed text-xs text-muted-foreground">
                {{ t('admin.dashboard.emptyChart') }}
              </div>
            </div>
          </CardContent>
        </Card>
      </div>

      <!-- Secondary info row -->
      <div class="grid gap-4 lg:grid-cols-3">
        <!-- Provider rankings -->
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Cpu class="size-4" />
              {{ t('admin.dashboard.providerRanking') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div v-if="data.providers.length === 0" class="rounded-lg border border-dashed p-4 text-center text-xs text-muted-foreground">
              {{ t('admin.dashboard.emptyProviders') }}
            </div>
            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>{{ t('admin.dashboard.cols.provider') }}</TableHead>
                  <TableHead class="text-right">{{ t('admin.dashboard.cols.calls') }}</TableHead>
                  <TableHead class="text-right">{{ t('admin.dashboard.cols.tokens') }}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="p in data.providers" :key="p.provider_name">
                  <TableCell class="font-medium">{{ p.provider_name }}</TableCell>
                  <TableCell class="text-right tabular-nums">{{ formatNum(p.calls) }}</TableCell>
                  <TableCell class="text-right tabular-nums">{{ tokenLabel(p.total_tokens) }}</TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </CardContent>
        </Card>

        <!-- Runner health -->
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Server class="size-4" />
              {{ t('admin.dashboard.runnerHealth') }}
            </CardTitle>
          </CardHeader>
          <CardContent class="space-y-3">
            <div class="flex items-center justify-between rounded-md bg-muted/50 px-4 py-3">
              <span class="text-sm">{{ t('admin.dashboard.healthOnline') }}</span>
              <Badge variant="secondary">{{ formatNum(data.health.online_runners) }}</Badge>
            </div>
            <div class="flex items-center justify-between rounded-md bg-muted/50 px-4 py-3">
              <span class="text-sm">{{ t('admin.dashboard.healthOffline') }}</span>
              <Badge variant="outline">{{ formatNum(data.health.offline_runners) }}</Badge>
            </div>
            <div class="flex items-center justify-between rounded-md bg-muted/50 px-4 py-3">
              <span class="text-sm">{{ t('admin.dashboard.healthDisabled') }}</span>
              <Badge variant="destructive">{{ formatNum(data.health.disabled_runners) }}</Badge>
            </div>
            <div class="flex items-center justify-between rounded-md bg-muted/50 px-4 py-3">
              <span class="text-sm">{{ t('admin.dashboard.healthLiveSessions') }}</span>
              <Badge variant="secondary">{{ formatNum(data.health.live_sessions) }}</Badge>
            </div>
          </CardContent>
        </Card>

        <!-- Recent failures -->
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <AlertTriangle class="size-4 text-destructive" />
              {{ t('admin.dashboard.recentFailures') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <div v-if="data.recent_failures.length === 0" class="rounded-lg border border-dashed p-4 text-center text-xs text-muted-foreground">
              {{ t('admin.dashboard.emptyFailures') }}
            </div>
            <div v-else class="space-y-3">
              <div
                v-for="f in data.recent_failures"
                :key="f.id"
                class="rounded-md border p-3 text-xs space-y-1.5"
              >
                <div class="flex items-start justify-between gap-2">
                  <span class="font-medium">{{ f.provider_name }} / {{ f.model }}</span>
                  <Badge :variant="failureStatusVariant(f)">{{ f.status_code }}</Badge>
                </div>
                <p class="text-muted-foreground line-clamp-2">{{ f.error_message || '—' }}</p>
                <div class="flex items-center justify-between text-muted-foreground">
                  <span>{{ formatDate(f.created_at) }}</span>
                  <code v-if="f.session_id" class="font-mono">#{{ f.session_id }}</code>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
    </template>

    <!-- Empty state: data loaded but empty across the board -->
    <template v-if="data && !loading && !error &&
      data.summary.total_calls === 0 &&
      data.timeseries.daily_calls.length === 0 &&
      data.providers.length === 0 &&
      data.recent_failures.length === 0"
    >
      <div class="rounded-lg border border-dashed p-12 text-center">
        <Calendar class="mx-auto size-8 text-muted-foreground" />
        <p class="mt-3 text-sm font-medium">{{ t('admin.dashboard.empty') }}</p>
        <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.dashboard.emptyHint') }}</p>
      </div>
    </template>
  </div>
</template>
