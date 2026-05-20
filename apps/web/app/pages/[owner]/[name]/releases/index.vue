<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Plus, Rocket } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { Release, ReleaseListResp } from '~/types/release'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.releases') },
  ]
})

const tabValues = ['all', 'draft', 'published'] as const
type TabValue = typeof tabValues[number]

const tab = ref<TabValue>('all')
const items = ref<Release[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const query: Record<string, any> = { limit: 50 }
    if (tab.value !== 'all') {
      query.draft = tab.value === 'draft'
    }
    const res = await $fetch<ReleaseListResp>(`/api/repos/${owner.value}/${name.value}/releases`, {
      credentials: 'include',
      query,
    })
    items.value = res.items ?? []
    total.value = res.total
  } catch (e: any) {
    error.value = e?.data?.error ?? t('release.listFailed')
    items.value = []
  } finally {
    loading.value = false
  }
}

watch(tab, () => { load() })

onMounted(load)

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function shortSha(s: string) { return s.slice(0, 7) }
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('release.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('release.subtitle') }}
        </p>
      </div>
      <Button @click="router.push(`/${owner}/${name}/releases/new`)">
        <Plus class="size-4" />
        {{ t('release.new') }}
      </Button>
    </header>

    <Tabs v-model="tab" class="space-y-4">
      <TabsList>
        <TabsTrigger value="all">
          {{ t('release.tabs.all') }}
        </TabsTrigger>
        <TabsTrigger value="draft">
          {{ t('release.tabs.draft') }}
        </TabsTrigger>
        <TabsTrigger value="published">
          {{ t('release.tabs.published') }}
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
            {{ t('release.empty') }} —
            <NuxtLink :to="`/${owner}/${name}/releases/new`" class="underline">
              {{ t('release.new') }}
            </NuxtLink>
          </p>
          <ul v-else class="divide-y">
            <li v-for="r in items" :key="r.id" class="hover:bg-muted/30">
              <NuxtLink
                :to="`/${owner}/${name}/releases/${r.id}`"
                class="flex items-start gap-3 px-4 py-3"
              >
                <Rocket class="mt-1 size-4 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1 space-y-1">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="truncate text-sm font-medium">{{ r.title || r.tag_name }}</span>
                    <Badge :variant="r.is_draft ? 'outline' : 'secondary'">
                      {{ r.is_draft ? t('release.draft') : t('release.published') }}
                    </Badge>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    <code class="font-mono">{{ r.tag_name }}</code>
                    <span class="mx-1">·</span>
                    <code class="font-mono text-[10px]">{{ shortSha(r.target_commit_sha) }}</code>
                    <span class="mx-1">·</span>
                    {{ r.is_draft ? rel(r.created_at) : rel(r.published_at) }}
                  </p>
                </div>
              </NuxtLink>
            </li>
          </ul>
        </CardContent>
      </Card>
    </Tabs>
  </div>
</template>
