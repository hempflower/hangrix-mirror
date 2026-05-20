<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { GitCompare } from 'lucide-vue-next'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import type { FileDiff, RepoRefs } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  const name = String(route.params.name ?? '')
  const base = `/${owner}/${name}`
  return [
    { label: owner, to: base },
    { label: name, to: base },
    { label: t('repo.tabs.compare') },
  ]
})
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
useHead({ title: () => `${t('repo.compare.title')} · ${owner.value}/${name.value} - ${t('app.name')}` })

const refs = ref<RepoRefs | null>(null)
const branches = computed(() => refs.value?.branches ?? [])
const tags = computed(() => refs.value?.tags ?? [])

const fromRef = ref(String(route.query.from ?? ''))
const toRef = ref(String(route.query.to ?? ''))

const diffs = ref<FileDiff[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

async function loadRefs() {
  try {
    refs.value = await $fetch<RepoRefs>(`/api/repos/${owner.value}/${name.value}/refs`, {
      credentials: 'include',
    })
    if (!toRef.value) toRef.value = refs.value.default_branch || ''
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.loadFailed')
  }
}

async function loadDiff() {
  diffs.value = []
  if (!fromRef.value || !toRef.value) return
  loading.value = true
  error.value = null
  try {
    const res = await $fetch<FileDiff[]>(`/api/repos/${owner.value}/${name.value}/diff`, {
      credentials: 'include',
      query: { from: fromRef.value, to: toRef.value },
    })
    diffs.value = res ?? []
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.compare.loadFailed')
  } finally {
    loading.value = false
  }
}

function syncQuery() {
  router.replace({
    query: {
      ...route.query,
      from: fromRef.value || undefined,
      to: toRef.value || undefined,
    },
  })
}

watch([fromRef, toRef], () => {
  syncQuery()
  loadDiff()
})

onMounted(async () => {
  await loadRefs()
  if (fromRef.value && toRef.value) await loadDiff()
})
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-2">
      <nav class="text-sm text-muted-foreground">
        <NuxtLink :to="`/${owner}/${name}`" class="hover:text-foreground">
          {{ owner }} / {{ name }}
        </NuxtLink>
        <span class="mx-1">/</span>
        <span class="font-medium text-foreground">{{ t('repo.compare.title') }}</span>
      </nav>
      <div class="space-y-1">
        <h1 class="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <GitCompare class="size-5" />
          {{ t('repo.compare.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('repo.compare.subtitle') }}
        </p>
      </div>
    </header>

    <Card class="gap-0 py-3">
      <CardContent class="flex flex-wrap items-end gap-4">
        <div class="space-y-1">
          <p class="text-xs uppercase text-muted-foreground">{{ t('repo.compare.from') }}</p>
          <Select v-model="fromRef">
            <SelectTrigger class="w-[220px]">
              <SelectValue :placeholder="t('repo.compare.from')" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup v-if="branches.length > 0">
                <SelectLabel>{{ t('repo.branches.title') }}</SelectLabel>
                <SelectItem v-for="b in branches" :key="`b-${b.name}`" :value="b.name">
                  {{ b.name }}
                </SelectItem>
              </SelectGroup>
              <SelectGroup v-if="tags.length > 0">
                <SelectLabel>{{ t('repo.tags.title') }}</SelectLabel>
                <SelectItem v-for="tg in tags" :key="`t-${tg.name}`" :value="tg.name">
                  {{ tg.name }}
                </SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>

        <span class="pb-2 text-muted-foreground">…</span>

        <div class="space-y-1">
          <p class="text-xs uppercase text-muted-foreground">{{ t('repo.compare.to') }}</p>
          <Select v-model="toRef">
            <SelectTrigger class="w-[220px]">
              <SelectValue :placeholder="t('repo.compare.to')" />
            </SelectTrigger>
            <SelectContent>
              <SelectGroup v-if="branches.length > 0">
                <SelectLabel>{{ t('repo.branches.title') }}</SelectLabel>
                <SelectItem v-for="b in branches" :key="`b-${b.name}`" :value="b.name">
                  {{ b.name }}
                </SelectItem>
              </SelectGroup>
              <SelectGroup v-if="tags.length > 0">
                <SelectLabel>{{ t('repo.tags.title') }}</SelectLabel>
                <SelectItem v-for="tg in tags" :key="`t-${tg.name}`" :value="tg.name">
                  {{ tg.name }}
                </SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </CardContent>
    </Card>

    <p v-if="error" class="text-sm text-destructive">
      {{ error }}
    </p>
    <p v-else-if="loading" class="text-sm text-muted-foreground">
      {{ t('common.loading') }}
    </p>

    <template v-else-if="fromRef && toRef">
      <Card v-if="diffs.length === 0" class="gap-0 py-0">
        <CardContent class="p-6 text-center text-sm text-muted-foreground">
          {{ t('repo.compare.noDiff') }}
        </CardContent>
      </Card>
      <template v-else>
        <h2 class="text-sm font-medium text-muted-foreground">
          {{ t('repo.commit.files') }} · {{ diffs.length }}
        </h2>
        <FileDiffList :diffs="diffs" />
      </template>
    </template>
  </div>
</template>
