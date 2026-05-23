<script setup lang="ts">
import { ref } from 'vue'
import {
  Paperclip,
  X,
  Plus,
  Loader2,
  File as FileIcon,
  Image,
  Video,
  Archive,
  FileText,
  AlertCircle,
} from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import type { PlatformAttachment } from '~/types/issue'

const { t } = useI18n()

const emit = defineEmits<{
  (e: 'insert', snippet: string): void
}>()

const uploading = ref(false)
const uploadError = ref<string | null>(null)
const attachments = ref<PlatformAttachment[]>([])

// Hidden file input for triggering native picker
const fileInput = ref<HTMLInputElement | null>(null)

function triggerFilePicker() {
  fileInput.value?.click()
}

async function onFileSelected(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return

  // Reset input so the same file can be re-selected
  input.value = ''

  uploadError.value = null
  uploading.value = true

  try {
    const form = new FormData()
    form.append('file', file)

    const res = await $fetch<PlatformAttachment>(
      '/api/attachments',
      {
        method: 'POST',
        credentials: 'include',
        body: form,
      },
    )
    attachments.value.push(res)
  } catch (e: any) {
    uploadError.value = e?.data?.error ?? t('issue.attachment.uploadFailed')
  } finally {
    uploading.value = false
  }
}

const deleting = ref<Set<number>>(new Set())
const deleteErrors = ref<Record<number, string>>({})

async function removeAttachment(att: PlatformAttachment) {
  deleting.value.add(att.id)
  try {
    await $fetch(
      `/api/attachments/${att.id}`,
      { method: 'DELETE', credentials: 'include' },
    )
  attachments.value = attachments.value.filter((a) => a.id !== att.id)
  delete deleteErrors.value[att.id]
  } catch (e: any) {
    // Keep the attachment in the list — server rejected the delete
    deleteErrors.value[att.id] =
      e?.data?.error ?? t('issue.attachment.removeFailed')
  } finally {
    deleting.value.delete(att.id)
  }
}

function insertAttachment(att: PlatformAttachment) {
  emit('insert', att.markdown_snippet || `[${att.display_name || att.original_name}](${att.url})`)
}

function kindIcon(kind?: string) {
  switch (kind) {
    case 'image': return Image
    case 'video': return Video
    case 'archive': return Archive
    case 'text': return FileText
    default: return FileIcon
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}
</script>

<template>
  <div class="space-y-2">
    <!-- Upload button + hidden file input -->
    <input
      ref="fileInput"
      type="file"
      class="hidden"
      @change="onFileSelected"
    />

    <div class="flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        :disabled="uploading"
        @click="triggerFilePicker"
      >
        <Loader2 v-if="uploading" class="size-3.5 animate-spin" />
        <Paperclip v-else class="size-3.5" />
        {{ uploading ? t('issue.attachment.uploading') : t('issue.attachment.attachFile') }}
      </Button>
    </div>

    <p v-if="uploadError" class="flex items-center gap-1 text-xs text-destructive">
      <AlertCircle class="size-3" />
      {{ uploadError }}
    </p>

    <!-- Uploaded attachments list -->
    <ul v-if="attachments.length > 0" class="space-y-1">
  <li
  v-for="att in attachments"
  :key="att.id"
  class="rounded border bg-muted/30 px-2.5 py-1.5 text-xs"
  >
  <div class="flex items-center gap-2">
  <component :is="kindIcon(att.kind)" class="size-3.5 shrink-0 text-muted-foreground" />
  <span class="min-w-0 flex-1 truncate font-mono">{{ att.original_name }}</span>
  <span class="shrink-0 text-muted-foreground">{{ formatSize(att.size_bytes ?? 0) }}</span>
  <Button
  variant="ghost"
  size="icon"
  class="size-6 text-muted-foreground hover:text-foreground"
  :title="t('issue.attachment.insert')"
  @click="insertAttachment(att)"
  >
  <Plus class="size-3.5" />
  </Button>
  <Button
  variant="ghost"
  size="icon"
  class="size-6 text-muted-foreground hover:text-destructive"
  :title="t('issue.attachment.remove')"
  :disabled="deleting.has(att.id)"
  @click="removeAttachment(att)"
  >
  <Loader2 v-if="deleting.has(att.id)" class="size-3.5 animate-spin" />
  <X v-else class="size-3.5" />
  </Button>
  </div>
  <p
  v-if="deleteErrors[att.id]"
  class="mt-1 flex items-center gap-1 text-destructive"
  >
  <AlertCircle class="size-3" />
  {{ deleteErrors[att.id] }}
  </p>
  </li>
    </ul>
  </div>
</template>
