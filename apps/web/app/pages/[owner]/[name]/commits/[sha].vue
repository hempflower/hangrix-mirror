<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { GitBranch, GitCommit, Tag } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import type { CommitWithDiff, ContainingRefs } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  const name = String(route.params.name ?? '')
  const sha = String(route.params.sha ?? '')
  const base = `/${owner}/${name}`
  return [
    { label: owner, to: base },
    { label: name, to: base },
    { label: t('repo.tabs.commits'), to: `${base}?tab=commits` },
    { label: sha.slice(0, 7) },
  ]
})
useHead({ title: () => `${String(route.params.sha ?? '').slice(0, 7)} · ${t('repo.commit.title')} · ${String(route.params.owner ?? '')}/${String(route.params.name ?? '')} - ${t('app.name')}` })

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const sha = computed(() => String(route.params.sha ?? ''))

const payload = ref<CommitWithDiff | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const contains = ref<ContainingRefs | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    payload.value = await $fetch<CommitWithDiff>(
      `/api/repos/${owner.value}/${name.value}/commits/${sha.value}`,
      { credentials: 'include' },
    )
    // Load "contained in" lazily after the commit body — never block the
    // page render on it; on failure we just don't show the panel.
    try {
      contains.value = await $fetch<ContainingRefs>(
        `/api/repos/${owner.value}/${name.value}/commits/${sha.value}/contains`,
        { credentials: 'include' },
      )
    } catch {
      contains.value = null
    }
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.loadFailed')
  } finally {
    loading.value = false
  }
}

function shortSha(s: string) {
  return s.slice(0, 7)
}

function formatDate(s: string) {
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

const diffList = computed(() => payload.value?.diff ?? [])

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <nav class="text-sm text-muted-foreground">
      <NuxtLink :to="`/${owner}/${name}`" class="hover:text-foreground">
        {{ owner }} / {{ name }}
      </NuxtLink>
      <span class="mx-1">/</span>
      <NuxtLink :to="`/${owner}/${name}?tab=commits`" class="hover:text-foreground">
        {{ t('repo.tabs.commits') }}
      </NuxtLink>
      <span class="mx-1">/</span>
      <code class="font-mono">{{ shortSha(sha) }}</code>
    </nav>

    <p v-if="error" class="text-sm text-destructive">
      {{ error }}
    </p>
    <p v-else-if="loading" class="text-sm text-muted-foreground">
      {{ t('common.loading') }}
    </p>

    <template v-if="payload">
      <Card class="gap-2 py-4">
        <CardHeader class="px-4">
          <div class="flex items-start gap-3">
            <GitCommit class="mt-1 size-5 text-muted-foreground" />
            <div class="min-w-0 flex-1 space-y-1">
              <CardTitle class="break-words text-base font-medium">
                <pre class="whitespace-pre-wrap font-sans text-base">{{ payload.commit.message }}</pre>
              </CardTitle>
              <CardDescription>
                <span class="font-medium text-foreground">{{ payload.commit.author.name }}</span>
                &lt;{{ payload.commit.author.email }}&gt;
                · {{ formatDate(payload.commit.committed_at) }}
              </CardDescription>
            </div>
            <code class="hidden font-mono text-xs text-muted-foreground sm:inline">{{ payload.commit.sha }}</code>
          </div>
        </CardHeader>
        <CardContent v-if="payload.commit.parent_shas && payload.commit.parent_shas.length > 0" class="flex flex-wrap items-center gap-2 px-4 text-xs text-muted-foreground">
          <span>{{ t('repo.commit.parents') }}:</span>
          <NuxtLink
            v-for="p in payload.commit.parent_shas"
            :key="p"
            :to="`/${owner}/${name}/commits/${p}`"
            class="font-mono hover:text-foreground"
          >
            {{ shortSha(p) }}
          </NuxtLink>
        </CardContent>
      </Card>

      <Card v-if="contains" class="gap-2 py-4">
        <CardHeader class="px-4">
          <CardTitle class="text-sm font-medium">
            {{ t('repo.containsRefs.title') }}
          </CardTitle>
        </CardHeader>
        <CardContent class="space-y-2 px-4">
          <div v-if="contains.branches.length > 0" class="flex flex-wrap items-center gap-2">
            <span class="text-xs text-muted-foreground">{{ t('repo.containsRefs.branches') }}:</span>
            <Badge
              v-for="b in contains.branches"
              :key="`b-${b.name}`"
              variant="secondary"
              class="font-mono text-xs"
            >
              <GitBranch class="size-3" />
              <NuxtLink :to="`/${owner}/${name}?ref=${encodeURIComponent(b.name)}`" class="hover:underline">
                {{ b.name }}
              </NuxtLink>
            </Badge>
          </div>
          <div v-if="contains.tags.length > 0" class="flex flex-wrap items-center gap-2">
            <span class="text-xs text-muted-foreground">{{ t('repo.containsRefs.tags') }}:</span>
            <Badge
              v-for="tg in contains.tags"
              :key="`t-${tg.name}`"
              variant="outline"
              class="font-mono text-xs"
            >
              <Tag class="size-3" />
              <NuxtLink :to="`/${owner}/${name}?ref=${encodeURIComponent(tg.name)}`" class="hover:underline">
                {{ tg.name }}
              </NuxtLink>
            </Badge>
          </div>
          <p
            v-if="contains.branches.length === 0 && contains.tags.length === 0"
            class="text-xs text-muted-foreground"
          >
            {{ t('repo.containsRefs.none') }}
          </p>
        </CardContent>
      </Card>

      <h2 class="text-sm font-medium text-muted-foreground">
        {{ t('repo.commit.files') }} · {{ diffList.length }}
      </h2>

      <!-- "before" ref is the first parent's SHA. Root commits have no
           parent — passing '' suppresses the before-link entirely, which is
           correct (every file shows up as added against an empty tree). -->
      <FileDiffList
        :diffs="diffList"
        :owner="owner"
        :name="name"
        :ref-before="payload.commit.parent_shas[0] ?? ''"
        :ref-after="payload.commit.sha"
      />
    </template>
  </div>
</template>
