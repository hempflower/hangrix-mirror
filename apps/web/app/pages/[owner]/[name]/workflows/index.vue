<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Play, Zap } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import Pagination from '@/components/ui/pagination/Pagination.vue'
import type { WorkflowDefinition, WorkflowRun, WorkflowRunListResp, WorkflowRunStatus } from '~/types/workflow'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

useHead({ title: () => `${owner.value}/${name.value} · ${t('repo.workflows.title')} - ${t('app.name')}` })

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.workflows.title') },
  ]
})

const tabValues = ['all', 'pending', 'running', 'success', 'failed', 'cancelled'] as const
type TabValue = typeof tabValues[number]

const tab = ref<TabValue>('all')
const items = ref<WorkflowRun[]>([])
const total = ref(0)
const offset = ref(0)
const limit = 50
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const query: Record<string, any> = { limit, offset: offset.value }
    if (tab.value !== 'all') query.status = tab.value
    const res = await $fetch<WorkflowRunListResp>(`/api/repos/${owner.value}/${name.value}/workflows/runs`, {
      credentials: 'include',
      query,
    })
    items.value = res.items ?? []
    total.value = res.total
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.workflows.listFailed')
    items.value = []
  } finally {
    loading.value = false
  }
}

watch(tab, () => { offset.value = 0; load() })

onMounted(load)

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function shortSha(s: string) { return s.slice(0, 7) }

function onPage(v: number) {
  offset.value = v
  load()
}

function statusVariant(s: WorkflowRunStatus) {
  switch (s) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    case 'running': return 'default' as const
    case 'cancelled': return 'outline' as const
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

// --- Dispatch dialog ---
const dispatchOpen = ref(false)
const dispatchDefs = ref<WorkflowDefinition[]>([])
const dispatchDefsLoading = ref(false)
const dispatchSelected = ref('')
const dispatchRef = ref('')
const dispatchError = ref<string | null>(null)
const dispatchSending = ref(false)

async function openDispatch() {
  dispatchOpen.value = true
  dispatchDefsLoading.value = true
  dispatchError.value = null
  dispatchSelected.value = ''
  dispatchRef.value = ''
  try {
    // Fetch workflow definitions to know which ones support dispatch
    const defs = await $fetch<WorkflowDefinition[]>(`/api/repos/${owner.value}/${name.value}/workflows/definitions`, {
      credentials: 'include',
    })
    dispatchDefs.value = defs.filter(d => d.on.includes('workflow.dispatch'))
  } catch (e: any) {
    dispatchError.value = e?.data?.error ?? t('repo.workflows.dispatchFailed')
  } finally {
    dispatchDefsLoading.value = false
  }
}

async function onDispatch() {
  if (!dispatchSelected.value) return
  dispatchSending.value = true
  dispatchError.value = null
  try {
    const body: Record<string, string> = {}
    if (dispatchRef.value.trim()) body.ref = dispatchRef.value.trim()
    await $fetch(`/api/repos/${owner.value}/${name.value}/workflows/${encodeURIComponent(dispatchSelected.value)}/dispatch`, {
      method: 'POST',
      credentials: 'include',
      body,
    })
    dispatchOpen.value = false
    // eslint-disable-next-line no-alert
    window.alert(t('repo.workflows.dispatchSuccess'))
    load()
  } catch (e: any) {
    dispatchError.value = e?.data?.error ?? t('repo.workflows.dispatchFailed')
  } finally {
    dispatchSending.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('repo.workflows.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('repo.workflows.subtitle') }}
        </p>
      </div>
      <Button @click="openDispatch">
        <Zap class="size-4" />
        {{ t('repo.workflows.dispatch') }}
      </Button>
    </header>

    <Tabs v-model="tab" class="space-y-4">
      <TabsList>
        <TabsTrigger value="all">
          {{ t('issue.filters.all') }}
        </TabsTrigger>
        <TabsTrigger value="pending">
          {{ t('repo.workflows.status.pending') }}
        </TabsTrigger>
        <TabsTrigger value="running">
          {{ t('repo.workflows.status.running') }}
        </TabsTrigger>
        <TabsTrigger value="success">
          {{ t('repo.workflows.status.success') }}
        </TabsTrigger>
        <TabsTrigger value="failed">
          {{ t('repo.workflows.status.failed') }}
        </TabsTrigger>
        <TabsTrigger value="cancelled">
          {{ t('repo.workflows.status.cancelled') }}
        </TabsTrigger>
      </TabsList>

      <p v-if="error" class="text-sm text-destructive">
        {{ error }}
      </p>

      <Card class="gap-0 py-0">
        <CardContent class="p-0">
          <p v-if="loading" class="p-4 text-sm text-muted-foreground">
            {{ t('common.loading') }}
          </p>
          <p v-else-if="items.length === 0" class="p-6 text-center text-sm text-muted-foreground">
            {{ t('repo.workflows.empty') }}
          </p>
          <ul v-else class="divide-y">
            <li v-for="run in items" :key="run.id" class="hover:bg-muted/30">
              <NuxtLink
                :to="`/${owner}/${name}/workflows/${run.id}`"
                class="flex items-start gap-3 px-4 py-3"
              >
                <Play class="mt-1 size-4 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1 space-y-1">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="truncate text-sm font-medium">{{ run.workflow_name }}</span>
                    <Badge :variant="statusVariant(run.status)">
                      {{ t(`repo.workflows.status.${run.status}`) }}
                    </Badge>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    {{ t(`repo.workflows.event.${run.event_name}`) }}
                    <span class="mx-1">·</span>
                    <code class="font-mono text-[10px]">{{ run.ref }}</code>
                    <span class="mx-1">·</span>
                    <code class="font-mono text-[10px]">{{ shortSha(run.commit_sha) }}</code>
                    <span class="mx-1">·</span>
                    {{ rel(run.created_at) }}
                    <template v-if="run.started_at">
                      <span class="mx-1">·</span>
                      {{ duration(run.started_at, run.finished_at) }}
                    </template>
                  </p>
                </div>
              </NuxtLink>
            </li>
          </ul>
        </CardContent>
      </Card>

      <Pagination
        v-if="total > limit"
        :total="total"
        :offset="offset"
        :limit="limit"
        @update:offset="onPage"
      />
    </Tabs>

    <!-- Dispatch dialog -->
    <Dialog v-model:open="dispatchOpen">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('repo.workflows.dispatchTitle') }}</DialogTitle>
          <DialogDescription>
            {{ t('repo.workflows.dispatchSubtitle') }}
          </DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <div v-if="dispatchDefsLoading" class="text-sm text-muted-foreground">
            {{ t('common.loading') }}
          </div>
          <template v-else>
            <div v-if="dispatchDefs.length === 0" class="text-sm text-muted-foreground">
              {{ t('repo.workflows.empty') }}
            </div>
            <template v-else>
              <div class="space-y-2">
                <Label>{{ t('repo.workflows.dispatchSelect') }}</Label>
                <Select v-model="dispatchSelected">
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem v-for="d in dispatchDefs" :key="d.name" :value="d.name">
                        {{ d.name }}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
              <div class="space-y-2">
                <Label for="dispatch-ref">{{ t('repo.workflows.dispatchRef') }}</Label>
                <Input id="dispatch-ref" v-model="dispatchRef" placeholder="main" />
                <p class="text-xs text-muted-foreground">
                  {{ t('repo.workflows.dispatchRefHint') }}
                </p>
              </div>
            </template>
          </template>
          <p v-if="dispatchError" class="text-sm text-destructive">{{ dispatchError }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="dispatchOpen = false">
            {{ t('common.cancel') }}
          </Button>
          <Button
            :disabled="!dispatchSelected || dispatchSending || dispatchDefsLoading"
            @click="onDispatch"
          >
            {{ dispatchSending ? t('common.submitting') : t('repo.workflows.dispatchSubmit') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
