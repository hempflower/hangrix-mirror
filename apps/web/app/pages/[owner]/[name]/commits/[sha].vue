<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { GitCommit } from 'lucide-vue-next'
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from '@/components/ui/card'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import type { CommitWithDiff } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const sha = computed(() => String(route.params.sha ?? ''))

const payload = ref<CommitWithDiff | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    payload.value = await $fetch<CommitWithDiff>(
      `/api/repos/${owner.value}/${name.value}/commits/${sha.value}`,
      { credentials: 'include' },
    )
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

      <h2 class="text-sm font-medium text-muted-foreground">
        {{ t('repo.commit.files') }} · {{ diffList.length }}
      </h2>

      <FileDiffList :diffs="diffList" />
    </template>
  </div>
</template>
