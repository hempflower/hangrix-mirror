<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CircleDot, GitMerge, Lock, Plus } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import Pagination from '@/components/ui/pagination/Pagination.vue'
import type { Issue, IssueListResp, IssueState } from '~/types/issue'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
useHead({ title: () => `${owner.value}/${name.value} · ${t('issue.title')} - ${t('app.name')}` })

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.issues') },
  ]
})

const PER_PAGE = 20

const tabValues = ['all', 'open', 'merged', 'closed'] as const
type TabValue = typeof tabValues[number]

function parseTab(s: string | undefined): TabValue {
  if (s && (tabValues as readonly string[]).includes(s)) return s as TabValue
  return 'open'
}
function parsePage(p: string | undefined): number {
  const n = Number(p)
  return Number.isInteger(n) && n >= 1 ? n : 1
}

// URL is the source of truth for state and page
const tab = computed<TabValue>(() => parseTab(String(route.query.state ?? '')))
const page = computed(() => parsePage(String(route.query.page ?? '')))
const offset = computed(() => (page.value - 1) * PER_PAGE)

function setTab(v: string | number) {
  const parsed = parseTab(String(v))
  const query: Record<string, any> = {}
  if (parsed !== 'open') query.state = parsed
  router.replace({ query })
}
function setOffset(newOffset: number) {
  const newPage = Math.floor(newOffset / PER_PAGE) + 1
  const query: Record<string, any> = {}
  if (tab.value !== 'open') query.state = tab.value
  if (newPage > 1) query.page = String(newPage)
  router.replace({ query })
}

const items = ref<Issue[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const query: Record<string, any> = { limit: PER_PAGE, offset: offset.value }
    if (tab.value !== 'all') query.state = tab.value
    const res = await $fetch<IssueListResp>(`/api/repos/${owner.value}/${name.value}/issues`, {
      credentials: 'include',
      query,
    })
    items.value = res.items ?? []
    total.value = res.total

    // Out-of-bounds page: redirect to page 1
    if (items.value.length === 0 && total.value > 0 && page.value > 1) {
      router.replace({ query: tab.value !== 'open' ? { state: tab.value } : {} })
    }
  } catch (e: any) {
    error.value = e?.data?.error ?? t('issue.listFailed')
    items.value = []
  } finally {
    loading.value = false
  }
}

watch([tab, page], () => { load() }, { immediate: true })

function badgeIcon(state: IssueState) {
  if (state === 'merged') return GitMerge
  if (state === 'closed') return Lock
  return CircleDot
}

function badgeClass(state: IssueState) {
  switch (state) {
    case 'open': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'merged': return 'bg-violet-500/15 text-violet-700 dark:text-violet-300'
    case 'closed': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function gotoNew() {
  router.push(`/${owner.value}/${name.value}/issues/new`)
}
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('issue.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('issue.subtitle') }}
        </p>
      </div>
      <Button @click="gotoNew">
        <Plus class="size-4" />
        {{ t('issue.new') }}
      </Button>
    </header>

    <Tabs :model-value="tab" @update:model-value="setTab" class="space-y-4">
      <TabsList>
        <TabsTrigger value="open">
          {{ t('issue.filters.open') }}
        </TabsTrigger>
        <TabsTrigger value="merged">
          {{ t('issue.filters.merged') }}
        </TabsTrigger>
        <TabsTrigger value="closed">
          {{ t('issue.filters.closed') }}
        </TabsTrigger>
        <TabsTrigger value="all">
          {{ t('issue.filters.all') }}
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
            {{ t('issue.empty') }} —
            <NuxtLink :to="`/${owner}/${name}/issues/new`" class="underline">
              {{ t('issue.new') }}
            </NuxtLink>
          </p>
          <ul v-else class="divide-y">
            <li v-for="iss in items" :key="iss.id" class="hover:bg-muted/30">
              <NuxtLink
                :to="`/${owner}/${name}/issues/${iss.number}`"
                class="flex items-start gap-3 px-4 py-3"
              >
                <component :is="badgeIcon(iss.state)" class="mt-1 size-4 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1 space-y-1">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="truncate text-sm font-medium">{{ iss.title }}</span>
                    <Badge :class="badgeClass(iss.state)" variant="secondary">
                      {{ t(`issue.state.${iss.state}`) }}
                    </Badge>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    #{{ iss.number }} ·
                    {{ t('issue.openedBy', { name: iss.author_username, time: rel(iss.created_at) }) }}
                  </p>
                </div>
                <code class="hidden font-mono text-xs text-muted-foreground sm:inline">
                  {{ iss.branch_name }}
                </code>
              </NuxtLink>
            </li>
          </ul>
        </CardContent>
      </Card>
      <Pagination
        v-if="!loading && total > 0"
        :total="total"
        :offset="offset"
        :limit="PER_PAGE"
        @update:offset="setOffset"
      />
    </Tabs>
  </div>
</template>
