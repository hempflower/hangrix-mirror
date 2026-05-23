<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import type { BlobResp, CommitContentsReq, CommitContentsResp } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

const { repo, load: loadRepo } = useRepo(() => owner.value, () => name.value)
const { refs: repoRefs, load: loadRefs } = useRepoRefs(() => owner.value, () => name.value)

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  const rawPath = route.params.path
  const segs = Array.isArray(rawPath)
    ? (rawPath as string[]).filter(Boolean)
    : String(rawPath ?? '').split('/').filter(Boolean)
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.edit.title') },
  ]
})

useHead({ title: () => {
    const rawPath = route.params.path
    const segs = Array.isArray(rawPath)
      ? (rawPath as string[]).filter(Boolean)
      : String(rawPath ?? '').split('/').filter(Boolean)
    const filename = segs.length > 0 ? segs[segs.length - 1] : ''
    return `${t('repo.edit.title')}: ${filename} · ${owner.value}/${name.value} - ${t('app.name')}`
  } })

const refName = computed(() => String(route.params.ref ?? ''))

const filePath = computed(() => {
  const raw = route.params.path
  if (!raw) return ''
  if (Array.isArray(raw)) return raw.join('/')
  return String(raw)
})

// --- Load blob content ---
const content = ref('')
const originalContent = ref('')
const blobLoading = ref(false)
const blobError = ref<string | null>(null)
const tooLarge = ref(false)
const binary = ref(false)

async function loadBlob() {
  blobLoading.value = true
  blobError.value = null
  tooLarge.value = false
  binary.value = false
  try {
    const blob = await $fetch<BlobResp>(`/api/repos/${owner.value}/${name.value}/blob`, {
      credentials: 'include',
      query: { ref: refName.value, path: filePath.value },
    })
    if (blob.binary) {
      binary.value = true
      return
    }
    const raw = atob(blob.content_base64)
    const bytes = new Uint8Array(raw.length)
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i)
    const text = new TextDecoder('utf-8', { fatal: false }).decode(bytes)
    content.value = text
    originalContent.value = text
  } catch (e: any) {
    if (e?.statusCode === 413 || e?.response?.status === 413) {
      tooLarge.value = true
    } else {
      blobError.value = e?.data?.error ?? t('repo.edit.loadFailed')
    }
  } finally {
    blobLoading.value = false
  }
}

// --- Permission ---
const canEdit = computed(() => {
  const perm = repo.value?.viewer_permission ?? ''
  return perm === 'write' || perm === 'manage'
})

// --- baseCommitSha: three-tier fallback ---
const baseCommitSha = computed(() => {
  if (!repoRefs.value) return ''

  // 1. Match branch ref
  const branch = repoRefs.value.branches?.find(b => b.name === refName.value)
  if (branch) return branch.sha

  // 2. Match tag ref (now always peeled commit SHA thanks to server #78)
  const tag = repoRefs.value.tags?.find(t => t.name === refName.value)
  if (tag) return tag.sha

  // 3. Raw 40-char hex commit SHA
  if (/^[0-9a-f]{40}$/i.test(refName.value)) return refName.value

  return ''
})

// --- Branch check ---
const currentRefIsBranch = computed(() => {
  if (!repoRefs.value) return false
  return repoRefs.value.branches?.some(b => b.name === refName.value) ?? false
})

// --- Commit form ---
const commitMessage = ref('')
const commitMode = ref<'current' | 'new'>('current')
const newBranchName = ref('')
const submitting = ref(false)
const submitError = ref<string | null>(null)
const success = ref(false)
const successBranch = ref('')
const successSha = ref('')
const conflict = ref(false)

// Force commitMode to 'new' when ref is not a branch
watch(currentRefIsBranch, (isBranch) => {
  if (!isBranch) {
    commitMode.value = 'new'
  }
}, { immediate: true })

const submitDisabled = computed(() => {
  if (!commitMessage.value.trim()) return true
  if (commitMode.value === 'new' && !newBranchName.value.trim()) return true
  if (commitMode.value === 'current' && !currentRefIsBranch.value) return true
  if (!baseCommitSha.value) return true
  return false
})

async function handleSubmit() {
  if (submitDisabled.value) return
  submitting.value = true
  submitError.value = null
  conflict.value = false

  const body: CommitContentsReq = {
    ref: refName.value,
    path: filePath.value,
    base_commit_sha: baseCommitSha.value,
    content_utf8: content.value,
    commit_message: commitMessage.value,
  }
  if (commitMode.value === 'new') {
    body.new_branch_name = newBranchName.value
  }

  try {
    const resp = await $fetch<CommitContentsResp>(
      `/api/repos/${owner.value}/${name.value}/contents/commit`,
      {
        method: 'POST',
        credentials: 'include',
        body,
      },
    )
    success.value = true
    successBranch.value = resp.branch
    successSha.value = resp.commit.sha.substring(0, 7)
  } catch (e: any) {
    if (e?.statusCode === 409 || e?.response?.status === 409) {
      conflict.value = true
    } else {
      submitError.value = e?.data?.error ?? t('repo.loadFailed')
    }
  } finally {
    submitting.value = false
  }
}

function goToBlob() {
  const encPath = filePath.value.split('/').map(encodeURIComponent).join('/')
  router.push(`/${owner.value}/${name.value}/blob/${encodeURIComponent(refName.value)}/${encPath}`)
}

function goToUpdatedBlob() {
  const targetRef = commitMode.value === 'new' ? newBranchName.value : refName.value
  const encPath = filePath.value.split('/').map(encodeURIComponent).join('/')
  router.push(`/${owner.value}/${name.value}/blob/${encodeURIComponent(targetRef)}/${encPath}`)
}

const unchanged = computed(() => content.value === originalContent.value)

onMounted(() => Promise.all([loadBlob(), loadRepo(), loadRefs()]))
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <!-- Error states (pre-content) -->
    <p v-if="blobError" class="text-sm text-destructive">{{ blobError }}</p>

    <!-- Permission denied -->
    <Card v-else-if="!canEdit" class="gap-0 py-3">
      <CardContent class="text-sm text-destructive">
        {{ t('repo.edit.noPermission') }}
      </CardContent>
    </Card>

    <!-- Too large -->
    <Card v-else-if="tooLarge" class="gap-0 py-3">
      <CardContent class="text-sm">
        {{ t('repo.edit.tooLarge') }}
      </CardContent>
    </Card>

    <!-- Binary -->
    <Card v-else-if="binary" class="gap-0 py-3">
      <CardContent class="text-sm text-muted-foreground">
        {{ t('repo.edit.binary') }}
      </CardContent>
    </Card>

    <!-- Loading -->
    <p v-else-if="blobLoading" class="text-sm text-muted-foreground">
      {{ t('repo.edit.loading') }}
    </p>

    <!-- Success -->
    <Card v-else-if="success" class="gap-0 py-3">
      <CardContent class="space-y-3 text-sm">
        <p class="font-medium text-green-600">{{ t('repo.edit.success') }}</p>
        <p class="text-muted-foreground">
          {{ t('repo.edit.successDetail', { sha: successSha, branch: successBranch }) }}
        </p>
        <Button variant="outline" size="sm" @click="goToUpdatedBlob">
          {{ t('repo.edit.viewFile') }}
        </Button>
      </CardContent>
    </Card>

    <!-- Edit form -->
    <template v-else>
      <!-- File info -->
      <p class="text-sm text-muted-foreground">
        {{ t('repo.edit.fileInfo', { path: filePath, ref: refName }) }}
      </p>

      <!-- Conflict banner -->
      <Card v-if="conflict" class="gap-0 border-destructive/50 bg-destructive/5 py-3">
        <CardContent class="space-y-1 text-sm">
          <p class="font-medium text-destructive">{{ t('repo.edit.conflict') }}</p>
          <p class="text-muted-foreground">{{ t('repo.edit.conflictHint') }}</p>
        </CardContent>
      </Card>

      <!-- Editor -->
      <Card class="gap-0 py-0">
        <CardContent class="p-0">
          <Textarea
            v-model="content"
            class="min-h-[400px] rounded-xl border-0 font-mono text-xs leading-5 focus-visible:ring-0"
            :disabled="submitting"
          />
        </CardContent>
      </Card>

      <!-- Commit form -->
      <Card class="gap-0 py-0">
        <CardContent class="space-y-4 p-4">
          <!-- Commit message -->
          <div class="space-y-2">
            <Label for="commit-message">{{ t('repo.edit.commitMessage') }}</Label>
            <Input
              id="commit-message"
              v-model="commitMessage"
              :placeholder="t('repo.edit.commitMessagePlaceholder')"
              :disabled="submitting"
            />
          </div>

          <!-- Branch target -->
          <div class="space-y-2">
            <Label>{{ t('repo.edit.commitToBranch', { branch: refName }) }}</Label>
            <RadioGroup v-model="commitMode" :disabled="submitting">
              <div v-if="currentRefIsBranch" class="flex items-center gap-2">
                <RadioGroupItem id="mode-current" value="current" />
                <Label for="mode-current" class="font-normal cursor-pointer">
                  {{ t('repo.edit.commitToBranch', { branch: refName }) }}
                </Label>
              </div>
              <div class="flex items-center gap-2">
                <RadioGroupItem id="mode-new" value="new" />
                <Label for="mode-new" class="font-normal cursor-pointer">
                  {{ t('repo.edit.createNewBranch') }}
                </Label>
              </div>
            </RadioGroup>
          </div>

          <!-- New branch name -->
          <div v-if="commitMode === 'new'" class="space-y-2">
            <Label for="new-branch-name">{{ t('repo.edit.newBranchName') }}</Label>
            <Input
              id="new-branch-name"
              v-model="newBranchName"
              :placeholder="t('repo.edit.newBranchNamePlaceholder')"
              :disabled="submitting"
            />
          </div>

          <!-- Submit error -->
          <p v-if="submitError" class="text-sm text-destructive">{{ submitError }}</p>

          <!-- Actions -->
          <div class="flex items-center gap-2">
            <Button
              :disabled="submitDisabled || submitting || unchanged"
              @click="handleSubmit"
            >
              {{ submitting ? t('common.loading') : t('repo.edit.submit') }}
            </Button>
            <Button variant="ghost" :disabled="submitting" @click="goToBlob">
              {{ t('repo.edit.cancel') }}
            </Button>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
