<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { ChevronRight, FileText, PencilLine } from 'lucide-vue-next'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import type { BlobResp } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  const name = String(route.params.name ?? '')
  const base = `/${owner}/${name}`
  const rawPath = route.params.path
  const segs = Array.isArray(rawPath)
    ? (rawPath as string[]).filter(Boolean)
    : String(rawPath ?? '').split('/').filter(Boolean)
  const out = [
    { label: owner, to: base },
    { label: name, to: base },
    { label: 'blob' },
  ]
  segs.forEach(seg => out.push({ label: seg }))
  return out
})

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

const { repo, load: loadRepo } = useRepo(() => owner.value, () => name.value)
const { refs: repoRefs, load: loadRefs } = useRepoRefs(() => owner.value, () => name.value)

useHead({ title: () => {
    const rawPath = route.params.path
    const segs = Array.isArray(rawPath)
      ? (rawPath as string[]).filter(Boolean)
      : String(rawPath ?? '').split('/').filter(Boolean)
    const filename = segs.length > 0 ? segs[segs.length - 1] : ''
    return `${filename} · ${owner.value}/${name.value} - ${t('app.name')}`
  } })

// Ref comes in as a single decoded segment — branches with `/` in the name
// are encoded as `%2F` on the way in (see entryHref in the index page) and
// Vue Router decodes them automatically here.
const refName = computed(() => String(route.params.ref ?? ''))

// Path is a catch-all (`[...path]`) so Nuxt gives us an array of decoded
// segments. Join with `/` to get the file path the backend expects.
const filePath = computed(() => {
  const raw = route.params.path
  if (!raw) return ''
  if (Array.isArray(raw)) return raw.join('/')
  return String(raw)
})

const blob = ref<BlobResp | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
const tooLarge = ref(false)

const breadcrumbParts = computed(() => {
  if (!filePath.value) return []
  const parts = filePath.value.split('/').filter(Boolean)
  const acc: { name: string; path: string }[] = []
  let p = ''
  for (const seg of parts) {
    p = p ? `${p}/${seg}` : seg
    acc.push({ name: seg, path: p })
  }
  return acc
})

const decodedText = computed(() => {
  if (!blob.value || blob.value.binary) return ''
  try {
    const raw = atob(blob.value.content_base64)
    const bytes = new Uint8Array(raw.length)
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
    return new TextDecoder('utf-8', { fatal: false }).decode(bytes)
  } catch {
    return ''
  }
})

const downloadHref = computed(() => {
  if (!blob.value) return '#'
  return `data:application/octet-stream;base64,${blob.value.content_base64}`
})

const downloadName = computed(() => {
  const parts = filePath.value.split('/')
  return parts[parts.length - 1] || 'file'
})

const canEdit = computed(() => {
  if (!blob.value || blob.value.binary) return false
  const perm = repo.value?.viewer_permission ?? ''
  return perm === 'write' || perm === 'manage'
})

function editUrl() {
  const encPath = filePath.value.split('/').map(encodeURIComponent).join('/')
  return `/${owner.value}/${name.value}/edit/${encodeURIComponent(refName.value)}/${encPath}`
}

// Tree links keep using the ?ref= query convention — they're served by
// /[owner]/[name]/index.vue which renders the file browser based on that
// query state.
function treeUrl(p: string) {
  const qs = new URLSearchParams()
  if (refName.value) qs.set('ref', refName.value)
  if (p) qs.set('path', p)
  return `/${owner.value}/${name.value}${qs.toString() ? `?${qs}` : ''}`
}

async function load() {
  loading.value = true
  error.value = null
  tooLarge.value = false
  blob.value = null
  try {
    blob.value = await $fetch<BlobResp>(`/api/repos/${owner.value}/${name.value}/blob`, {
      credentials: 'include',
      query: { ref: refName.value, path: filePath.value },
    })
  } catch (e: any) {
    if (e?.statusCode === 413 || e?.response?.status === 413) {
      tooLarge.value = true
    } else {
      error.value = e?.data?.error ?? t('repo.loadFailed')
    }
  } finally {
    loading.value = false
  }
}

watch([refName, filePath], load)
onMounted(() => Promise.all([load(), loadRepo(), loadRefs()]))
</script>

<template>
  <div class="space-y-4">
    <nav class="flex flex-wrap items-center gap-1 text-sm">
      <NuxtLink
        :to="treeUrl('')"
        class="text-muted-foreground hover:text-foreground"
      >
        {{ owner }} / {{ name }}
      </NuxtLink>
      <ChevronRight class="size-3 text-muted-foreground" />
      <NuxtLink
        :to="treeUrl('')"
        class="text-muted-foreground hover:text-foreground"
      >
        {{ t('repo.files.rootBreadcrumb') }}
      </NuxtLink>
      <template v-for="(part, idx) in breadcrumbParts" :key="part.path">
        <ChevronRight class="size-3 text-muted-foreground" />
        <NuxtLink
          v-if="idx < breadcrumbParts.length - 1"
          :to="treeUrl(part.path)"
          class="text-muted-foreground hover:text-foreground"
        >
          {{ part.name }}
        </NuxtLink>
        <span v-else class="font-medium">{{ part.name }}</span>
      </template>
    </nav>

    <p v-if="error" class="text-sm text-destructive">
      {{ error }}
    </p>
    <p v-else-if="loading" class="text-sm text-muted-foreground">
      {{ t('common.loading') }}
    </p>

    <Card v-else-if="tooLarge" class="gap-0 py-3">
      <CardContent class="text-sm">
        <p class="font-medium">{{ t('repo.files.tooLarge') }}</p>
      </CardContent>
    </Card>

    <Card v-else-if="blob" class="gap-0 py-0">
      <CardContent class="p-0">
        <div class="flex items-center justify-between rounded-t-xl border-b bg-muted/40 px-4 py-2">
          <div class="flex items-center gap-2 text-sm">
            <FileText class="size-4 text-muted-foreground" />
            <span class="font-mono">{{ downloadName }}</span>
            <span class="text-xs text-muted-foreground">
              · {{ t('repo.files.size', { n: blob.size }) }}
            </span>
          </div>
          <div class="flex items-center gap-2">
              <Button
                v-if="canEdit"
                variant="ghost"
                size="sm"
                as="NuxtLink"
                :to="editUrl()"
              >
                <PencilLine class="size-4" />
                {{ t('repo.edit.editButton') }}
              </Button>
              <a
                v-if="blob.binary"
                :href="downloadHref"
                :download="downloadName"
                class="text-sm text-primary hover:underline"
              >
                {{ t('repo.files.download') }}
              </a>
            </div>
        </div>
        <div v-if="blob.binary" class="p-4 text-sm text-muted-foreground">
          {{ t('repo.files.binary') }} · {{ t('repo.files.size', { n: blob.size }) }}
        </div>
        <pre v-else class="overflow-x-auto whitespace-pre p-4 font-mono text-xs leading-5"><code>{{ decodedText }}</code></pre>
      </CardContent>
    </Card>
  </div>
</template>
