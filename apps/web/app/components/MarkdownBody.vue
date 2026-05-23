<script setup lang="ts">
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

const props = withDefaults(defineProps<{
  source: string
  // GitHub-style soft line breaks: `true` matches issue/comment conventions
  // where a newline becomes <br>. Long-form docs (READMEs) leave it `false`
  // so wrapped paragraphs render as one block.
  breaks?: boolean
}>(), {
  breaks: true,
})

const html = computed(() => {
  if (!props.source) return ''
  const raw = marked.parse(props.source, { gfm: true, breaks: props.breaks, async: false }) as string
  return DOMPurify.sanitize(raw)
})
</script>

<template>
  <div
    class="markdown-body prose prose-sm dark:prose-invert max-w-none"
    v-html="html"
  />
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
.markdown-body :deep(p:first-child) { margin-top: 0; }
.markdown-body :deep(p:last-child) { margin-bottom: 0; }
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
