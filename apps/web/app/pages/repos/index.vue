<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ArrowRight, FolderGit2, Plus } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import type { PublicRepo, RepoListResp } from '~/types/repo'

const { t } = useI18n()

setBreadcrumbs(() => [{ label: t('repo.title') }])
useHead({ title: () => `${t('repo.title')} - ${t('app.name')}` })

const repos = ref<PublicRepo[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await $fetch<RepoListResp>('/api/repos/me', { credentials: 'include' })
    repos.value = res.items
    total.value = res.total
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.loadFailed')
  } finally {
    loading.value = false
  }
}

function formatDate(s: string) {
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="flex items-start justify-between gap-4">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('repo.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('repo.subtitle') }} · {{ t('common.total', { n: total }) }}
        </p>
      </div>
      <Button as-child>
        <NuxtLink to="/repos/new">
          <Plus class="size-4" />
          {{ t('repo.create') }}
        </NuxtLink>
      </Button>
    </header>

    <p v-if="error" class="text-sm text-destructive">
      {{ error }}
    </p>

    <p v-if="loading && !repos.length" class="text-sm text-muted-foreground">
      {{ t('common.loading') }}
    </p>

    <section v-if="!loading && repos.length === 0" class="rounded-lg border border-dashed p-10 text-center">
      <FolderGit2 class="mx-auto size-10 text-muted-foreground" />
      <h2 class="mt-4 text-lg font-medium">
        {{ t('repo.empty') }}
      </h2>
      <p class="mt-1 text-sm text-muted-foreground">
        {{ t('repo.emptyHint') }}
      </p>
      <Button class="mt-6" as-child>
        <NuxtLink to="/repos/new">
          <Plus class="size-4" />
          {{ t('repo.create') }}
        </NuxtLink>
      </Button>
    </section>

    <section v-else class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <Card v-for="r in repos" :key="r.id" class="transition-shadow hover:shadow-md">
        <CardHeader>
          <div class="flex items-center justify-between gap-2">
            <CardTitle class="truncate text-base">
              <NuxtLink :to="`/${r.owner_username}/${r.name}`" class="hover:underline">
                {{ r.owner_username }} / {{ r.name }}
              </NuxtLink>
            </CardTitle>
            <Badge :variant="r.visibility === 'private' ? 'outline' : 'secondary'">
              {{ t(`repo.visibility${r.visibility === 'private' ? 'Private' : 'Public'}`) }}
            </Badge>
          </div>
          <CardDescription class="line-clamp-2 min-h-[2.5rem]">
            {{ r.description || '—' }}
          </CardDescription>
        </CardHeader>
        <CardContent class="flex items-center justify-between text-xs text-muted-foreground">
          <span class="truncate">{{ formatDate(r.updated_at) }}</span>
          <NuxtLink
            :to="`/${r.owner_username}/${r.name}`"
            class="inline-flex items-center gap-1 text-foreground hover:text-primary"
          >
            <span>{{ r.default_branch }}</span>
            <ArrowRight class="size-3" />
          </NuxtLink>
        </CardContent>
      </Card>
    </section>
  </div>
</template>
