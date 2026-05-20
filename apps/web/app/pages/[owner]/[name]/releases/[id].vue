<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  Download,
  FileText,
  Pencil,
  Rocket,
  Trash2,
  Upload,
  Zap,
} from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import MarkdownBody from '@/components/MarkdownBody.vue'
import type { Release, ReleaseAsset } from '~/types/release'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const id = computed(() => Number(route.params.id ?? 0))

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.releases'), to: `${base}/releases` },
    { label: release.value?.title || release.value?.tag_name || `#${id.value}` },
  ]
})

const release = ref<Release | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const editMode = ref(false)
const editTitle = ref('')
const editNotes = ref('')
const editTagName = ref('')
const editError = ref<string | null>(null)
const editSaving = ref(false)

const uploadFile = ref<File | null>(null)
const uploadError = ref<string | null>(null)
const uploadSaving = ref(false)

const { refs, load: loadRefs } = useRepoRefs(() => owner.value, () => name.value)
const tags = computed(() => refs.value?.tags ?? [])

async function load() {
  loading.value = true
  error.value = null
  try {
    release.value = await $fetch<Release>(`/api/repos/${owner.value}/${name.value}/releases/${id.value}`, {
      credentials: 'include',
    })
  } catch (e: any) {
    error.value = e?.data?.error ?? t('release.loadFailed')
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await Promise.all([load(), loadRefs()])
})

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function shortSha(s: string) { return s.slice(0, 7) }

function humanSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let size = bytes / 1024
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit++
  }
  return `${size.toFixed(1)} ${units[unit]}`
}

function startEdit() {
  if (!release.value) return
  editTitle.value = release.value.title
  editNotes.value = release.value.notes
  editTagName.value = release.value.tag_name
  editError.value = null
  editMode.value = true
}

function cancelEdit() {
  editMode.value = false
  editError.value = null
}

async function onSaveEdit() {
  if (!release.value) return
  editSaving.value = true
  editError.value = null
  const body: Record<string, string> = {}
  if (editTitle.value !== release.value.title) body.title = editTitle.value
  if (editNotes.value !== release.value.notes) body.notes = editNotes.value
  if (release.value.is_draft && editTagName.value !== release.value.tag_name) {
    body.tag_name = editTagName.value
  }
  try {
    const updated = await $fetch<Release>(`/api/repos/${owner.value}/${name.value}/releases/${id.value}`, {
      method: 'PATCH',
      credentials: 'include',
      body,
    })
    release.value = updated
    editMode.value = false
  } catch (e: any) {
    editError.value = e?.data?.error ?? t('release.saveFailed')
  } finally {
    editSaving.value = false
  }
}

async function onPublish() {
  if (!release.value) return
  if (!window.confirm(t('release.publishConfirm'))) return
  try {
    const updated = await $fetch<Release>(`/api/repos/${owner.value}/${name.value}/releases/${id.value}/publish`, {
      method: 'POST',
      credentials: 'include',
    })
    release.value = updated
  } catch (e: any) {
    alert(e?.data?.error ?? t('release.publishFailed'))
  }
}

async function onDelete() {
  if (!release.value) return
  if (!window.confirm(t('release.deleteConfirm', { title: release.value.title || release.value.tag_name }))) return
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/releases/${id.value}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    router.push(`/${owner.value}/${name.value}/releases`)
  } catch (e: any) {
    alert(e?.data?.error ?? t('release.deleteFailed'))
  }
}

function onFileChange(e: Event) {
  const input = e.target as HTMLInputElement
  uploadFile.value = input.files?.[0] ?? null
}

async function onUploadAsset() {
  if (!uploadFile.value || !release.value) return
  uploadSaving.value = true
  uploadError.value = null
  try {
    const form = new FormData()
    form.append('file', uploadFile.value)
    const updated = await $fetch<Release>(`/api/repos/${owner.value}/${name.value}/releases/${id.value}/assets`, {
      method: 'POST',
      credentials: 'include',
      body: form,
    })
    release.value = updated
    uploadFile.value = null
    // Reset file input
    const input = document.getElementById('asset-upload') as HTMLInputElement
    if (input) input.value = ''
  } catch (e: any) {
    uploadError.value = e?.data?.error ?? t('release.assets.uploadFailed')
  } finally {
    uploadSaving.value = false
  }
}

async function onDeleteAsset(asset: ReleaseAsset) {
  if (!window.confirm(t('release.assets.deleteConfirm', { name: asset.name }))) return
  try {
    const updated = await $fetch<Release>(
      `/api/repos/${owner.value}/${name.value}/releases/${id.value}/assets/${asset.id}`,
      {
        method: 'DELETE',
        credentials: 'include',
      },
    )
    release.value = updated
  } catch (e: any) {
    alert(e?.data?.error ?? t('release.assets.deleteFailed'))
  }
}
</script>

<template>
  <div class="mx-auto max-w-3xl space-y-6">
    <!-- Loading -->
    <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <!-- Error -->
    <div v-else-if="error || !release" class="space-y-2">
      <p class="text-sm text-destructive">{{ error || t('release.loadFailed') }}</p>
      <Button variant="outline" as-child>
        <NuxtLink :to="`/${owner}/${name}/releases`">
          {{ t('repo.tabs2.releases') }}
        </NuxtLink>
      </Button>
    </div>

    <template v-else>
      <!-- Header -->
      <header class="space-y-3">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="space-y-1">
            <h1 class="text-2xl font-semibold tracking-tight">
              {{ release.title || release.tag_name }}
            </h1>
            <p class="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
              <Badge :variant="release.is_draft ? 'outline' : 'secondary'">
                {{ release.is_draft ? t('release.draft') : t('release.published') }}
              </Badge>
              <code class="font-mono">{{ release.tag_name }}</code>
              <span>·</span>
              <code class="font-mono text-xs">{{ shortSha(release.target_commit_sha) }}</code>
              <span>·</span>
              <span>{{ release.is_draft ? rel(release.created_at) : rel(release.published_at) }}</span>
            </p>
          </div>
          <div class="flex items-center gap-2">
            <Button v-if="release.is_draft" @click="onPublish">
              <Zap class="size-4" />
              {{ t('release.publish') }}
            </Button>
            <Button v-if="!editMode" variant="outline" @click="startEdit">
              <Pencil class="size-4" />
              {{ t('release.edit') }}
            </Button>
            <Button variant="outline" class="text-destructive hover:text-destructive" @click="onDelete">
              <Trash2 class="size-4" />
              {{ t('release.delete') }}
            </Button>
          </div>
        </div>
      </header>

      <!-- Edit form -->
      <Card v-if="editMode">
        <CardHeader>
          <CardTitle>{{ t('release.editTitle') }}</CardTitle>
        </CardHeader>
        <CardContent class="space-y-4">
          <div v-if="release.is_draft" class="space-y-2">
            <label class="text-sm font-medium">{{ t('release.fields.tag') }}</label>
            <Select v-model="editTagName">
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectLabel>{{ t('repo.tabs.tags') }}</SelectLabel>
                  <SelectItem v-for="tg in tags" :key="tg.name" :value="tg.name">
                    {{ tg.name }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <label class="text-sm font-medium">{{ t('release.fields.title') }}</label>
            <Input v-model="editTitle" />
          </div>
          <div class="space-y-2">
            <label class="text-sm font-medium">{{ t('release.fields.notes') }}</label>
            <Textarea v-model="editNotes" rows="8" />
          </div>
          <p v-if="editError" class="text-sm text-destructive">{{ editError }}</p>
          <div class="flex items-center gap-3">
            <Button :disabled="editSaving" @click="onSaveEdit">
              {{ editSaving ? t('release.saving') : t('release.save') }}
            </Button>
            <Button variant="outline" @click="cancelEdit">
              {{ t('common.cancel') }}
            </Button>
          </div>
        </CardContent>
      </Card>

      <!-- Release notes -->
      <Card v-if="!editMode && release.notes">
        <CardHeader class="pb-3">
          <CardTitle class="text-base">{{ t('release.fields.notes') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <MarkdownBody :source="release.notes" />
        </CardContent>
      </Card>

      <!-- Assets -->
      <Card>
        <CardHeader class="pb-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <CardTitle class="text-base">{{ t('release.assets.title') }}</CardTitle>
          </div>
        </CardHeader>
        <CardContent class="space-y-4">
          <!-- Source archives -->
          <div>
            <h4 class="mb-2 text-sm font-medium">{{ t('release.assets.source') }}</h4>
            <div class="flex flex-wrap gap-2">
              <Button
                v-for="sa in release.source_archives"
                :key="sa.format"
                variant="outline"
                size="sm"
                as-child
              >
                <a :href="sa.url" download>
                  <Download class="size-3.5" />
                  {{ sa.format }}
                </a>
              </Button>
              <p v-if="!release.source_archives?.length" class="text-xs text-muted-foreground">
                —
              </p>
            </div>
          </div>

          <!-- Custom assets -->
          <div>
            <h4 class="mb-2 text-sm font-medium">{{ t('release.assets.custom') }}</h4>
            <ul v-if="release.assets?.length" class="divide-y rounded-md border">
              <li
                v-for="asset in release.assets"
                :key="asset.id"
                class="flex items-center gap-3 px-3 py-2.5"
              >
                <FileText class="size-4 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1">
                  <p class="truncate text-sm font-medium">{{ asset.name }}</p>
                  <p class="text-xs text-muted-foreground">{{ humanSize(asset.size_bytes) }}</p>
                </div>
                <Button variant="outline" size="sm" as-child>
                  <a
                    :href="`/api/repos/${owner}/${name}/releases/${id}/assets/${asset.id}/download`"
                    download
                  >
                    <Download class="size-3.5" />
                  </a>
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  class="text-destructive hover:text-destructive"
                  @click="onDeleteAsset(asset)"
                >
                  <Trash2 class="size-3.5" />
                </Button>
              </li>
            </ul>
            <p v-else class="text-xs text-muted-foreground">{{ t('release.assets.noAssets') }}</p>
          </div>

          <!-- Upload -->
          <div class="flex flex-wrap items-end gap-3 rounded-md border p-3">
            <div class="flex-1 space-y-1.5">
              <label for="asset-upload" class="text-sm font-medium">{{ t('release.assets.upload') }}</label>
              <Input id="asset-upload" type="file" @change="onFileChange" />
            </div>
            <Button :disabled="!uploadFile || uploadSaving" @click="onUploadAsset">
              <Upload class="size-4" />
              {{ uploadSaving ? t('release.assets.uploading') : t('release.assets.upload') }}
            </Button>
          </div>
          <p v-if="uploadError" class="text-sm text-destructive">{{ uploadError }}</p>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
