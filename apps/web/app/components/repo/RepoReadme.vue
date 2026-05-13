<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { FileText } from 'lucide-vue-next'
import type { BlobResp, TreeEntry } from '~/types/repo'

const props = defineProps<{
  owner: string
  name: string
  refName: string
  tree: TreeEntry[]
}>()

const { t } = useI18n()

marked.use({ gfm: true, breaks: false })

const README_PATTERN = /^readme\.(md|markdown|mdx)$/i

const readmeEntry = computed<TreeEntry | null>(() => {
  for (const e of props.tree) {
    if (e.kind !== 'blob' && e.kind !== 'executable') continue
    if (README_PATTERN.test(e.name)) return e
  }
  return null
})

const html = ref('')
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
  html.value = ''
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
    const md = decode(blob)
    const rendered = await marked.parse(md)
    html.value = DOMPurify.sanitize(rendered)
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
      <div
        v-else
        class="markdown-body prose prose-sm dark:prose-invert max-w-none"
        v-html="html"
      />
    </CardContent>
  </Card>
</template>

<style scoped>
.markdown-body :deep(h1) {
  font-size: 1.5rem;
  font-weight: 600;
  margin-top: 1rem;
  margin-bottom: 0.75rem;
  padding-bottom: 0.25rem;
  border-bottom: 1px solid var(--border);
}
.markdown-body :deep(h2) {
  font-size: 1.25rem;
  font-weight: 600;
  margin-top: 1rem;
  margin-bottom: 0.5rem;
  padding-bottom: 0.25rem;
  border-bottom: 1px solid var(--border);
}
.markdown-body :deep(h3) {
  font-size: 1.1rem;
  font-weight: 600;
  margin-top: 0.75rem;
  margin-bottom: 0.5rem;
}
.markdown-body :deep(p) {
  margin: 0.5rem 0;
  line-height: 1.6;
}
.markdown-body :deep(a) {
  color: var(--primary);
  text-decoration: underline;
}
.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  padding-left: 1.5rem;
  margin: 0.5rem 0;
}
.markdown-body :deep(ul) { list-style: disc; }
.markdown-body :deep(ol) { list-style: decimal; }
.markdown-body :deep(li) { margin: 0.25rem 0; }
.markdown-body :deep(code) {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 0.85em;
  background: color-mix(in srgb, var(--muted) 60%, transparent);
  padding: 0.1em 0.3em;
  border-radius: 0.25rem;
}
.markdown-body :deep(pre) {
  background: color-mix(in srgb, var(--muted) 60%, transparent);
  padding: 0.75rem 1rem;
  border-radius: 0.375rem;
  overflow-x: auto;
  margin: 0.75rem 0;
}
.markdown-body :deep(pre code) {
  background: transparent;
  padding: 0;
}
.markdown-body :deep(blockquote) {
  border-left: 3px solid var(--border);
  color: var(--muted-foreground);
  padding-left: 0.75rem;
  margin: 0.5rem 0;
}
.markdown-body :deep(table) {
  border-collapse: collapse;
  margin: 0.5rem 0;
}
.markdown-body :deep(th),
.markdown-body :deep(td) {
  border: 1px solid var(--border);
  padding: 0.25rem 0.5rem;
}
.markdown-body :deep(hr) {
  border: 0;
  border-top: 1px solid var(--border);
  margin: 1rem 0;
}
.markdown-body :deep(img) {
  max-width: 100%;
  height: auto;
}
</style>
