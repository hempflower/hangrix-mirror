<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { FileText } from 'lucide-vue-next'
import MarkdownBody from '@/components/MarkdownBody.vue'
import type { BlobResp, TreeEntry } from '~/types/repo'

const props = defineProps<{
  owner: string
  name: string
  refName: string
  tree: TreeEntry[]
}>()

const { t } = useI18n()

const README_PATTERN = /^readme\.(md|markdown|mdx)$/i

const readmeEntry = computed<TreeEntry | null>(() => {
  for (const e of props.tree) {
    if (e.kind !== 'blob' && e.kind !== 'executable') continue
    if (README_PATTERN.test(e.name)) return e
  }
  return null
})

const source = ref('')
const loading = ref(false)
const error = ref<string | null>(null)

function decode(b: BlobResp): string {
  try {
    const raw = atob(b.content_base64)
    const bytes = new Uint8Array(raw.length)
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
    return new TextDecoder('utf-8', { fatal: false }).decode(bytes)
  } catch {
    return ''
  }
}

async function load() {
  source.value = ''
  error.value = null
  const entry = readmeEntry.value
  if (!entry) return
  loading.value = true
  try {
    const blob = await $fetch<BlobResp>(
      `/api/repos/${props.owner}/${props.name}/blob`,
      {
        credentials: 'include',
        query: { ref: props.refName, path: entry.path },
      },
    )
    if (blob.binary) return
    source.value = decode(blob)
  } catch (e: any) {
    error.value = e?.data?.error ?? t('repo.loadFailed')
  } finally {
    loading.value = false
  }
}

watch(() => [props.owner, props.name, props.refName, readmeEntry.value?.path], () => {
  load()
}, { immediate: true })
</script>

<template>
  <Card v-if="readmeEntry" class="gap-0 py-0">
    <CardHeader class="rounded-t-xl border-b bg-muted/40 px-4 py-2">
      <CardTitle class="flex items-center gap-2 text-sm font-medium">
        <FileText class="size-4 text-muted-foreground" />
        {{ readmeEntry.name }}
      </CardTitle>
    </CardHeader>
    <CardContent class="px-4 py-3">
      <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>
      <p v-else-if="error" class="text-sm text-destructive">{{ error }}</p>
      <MarkdownBody v-else :source="source" :breaks="false" />
    </CardContent>
  </Card>
</template>
