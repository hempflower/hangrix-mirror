<script setup lang="ts">
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { Download, ExternalLink, FileWarning } from 'lucide-vue-next'
import type { IssueAttachment } from '~/types/issue'

const props = withDefaults(defineProps<{
  source: string
  // GitHub-style soft line breaks: `true` matches issue/comment conventions
  // where a newline becomes <br>. Long-form docs (READMEs) leave it `false`
  // so wrapped paragraphs render as one block.
  breaks?: boolean
  // Attachments map (id -> attachment) for rendering [attachment:N] tokens.
  attachments?: Record<number, IssueAttachment>
}>(), {
  breaks: true,
})

// Regex to match [attachment:N] and ![attachment:N] tokens (N is an integer id).
const ATTACH_RE = /(!?)\[attachment:(\d+)\]/g

function buildAttachmentCard(att: IssueAttachment, inline: boolean): string {
  if (att.status === 'deleted') {
    return `<div class="attachment-card attachment-deleted flex items-center gap-2 rounded-md border border-dashed border-muted-foreground/40 bg-muted/20 px-3 py-2 my-1 text-xs text-muted-foreground">
      <span class="inline-flex">⚠</span>
      <span>Attachment "${escapeHtml(att.original_name)}" has been deleted</span>
    </div>`
  }

  const isImage = att.kind === 'image'
  const isVideo = att.kind === 'video'
  const showPreview = inline && (isImage || isVideo)

  if (showPreview && isImage && att.preview_url) {
    return `<div class="attachment-card my-2">
      <a href="${escapeHtml(att.download_url)}" target="_blank" rel="noopener" class="block">
        <img src="${escapeHtml(att.preview_url)}" alt="${escapeHtml(att.original_name)}" class="max-w-full max-h-96 rounded-md border" loading="lazy" />
      </a>
      <div class="flex items-center gap-2 mt-1 text-xs text-muted-foreground">
        <span class="truncate">${escapeHtml(att.original_name)}</span>
        <span>${formatSize(att.size_bytes)}</span>
        <a href="${escapeHtml(att.download_url)}" class="hover:text-foreground underline" download>Download</a>
      </div>
    </div>`
  }

  if (showPreview && isVideo && att.preview_url) {
    return `<div class="attachment-card my-2">
      <video src="${escapeHtml(att.preview_url)}" controls class="max-w-full max-h-96 rounded-md border" preload="metadata"></video>
      <div class="flex items-center gap-2 mt-1 text-xs text-muted-foreground">
        <span class="truncate">${escapeHtml(att.original_name)}</span>
        <span>${formatSize(att.size_bytes)}</span>
        <a href="${escapeHtml(att.download_url)}" class="hover:text-foreground underline" download>Download</a>
      </div>
    </div>`
  }

  // Generic card: file icon + name + size + download
  const kindLabel = att.kind.charAt(0).toUpperCase() + att.kind.slice(1)
  return `<div class="attachment-card flex items-center gap-2 rounded-md border bg-muted/30 px-3 py-2 my-1 text-xs">
    <span class="shrink-0 font-mono text-muted-foreground">${kindLabel}</span>
    <span class="min-w-0 flex-1 truncate font-medium">${escapeHtml(att.original_name)}</span>
    <span class="shrink-0 text-muted-foreground">${formatSize(att.size_bytes)}</span>
    <a href="${escapeHtml(att.download_url)}" class="shrink-0 hover:text-foreground underline" download>Download</a>
  </div>`
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

const processedSource = computed(() => {
  if (!props.source) return ''
  const atts = props.attachments ?? {}
  return props.source.replace(ATTACH_RE, (_match, bang: string, idStr: string) => {
    const id = Number(idStr)
    const att = atts[id]
    const inline = bang === '!'
    if (!att) {
      // Unknown attachment — show a placeholder
      return `<span class="attachment-card inline-flex items-center gap-1 rounded border border-dashed border-muted-foreground/40 bg-muted/20 px-1.5 py-0.5 text-xs text-muted-foreground">
        <span>📎</span> Unknown attachment #${id}
      </span>`
    }
    return buildAttachmentCard(att, inline)
  })
})

const html = computed(() => {
  if (!processedSource.value) return ''
  const raw = marked.parse(processedSource.value, { gfm: true, breaks: props.breaks, async: false }) as string
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
