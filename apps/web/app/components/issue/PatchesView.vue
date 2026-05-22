<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import {
  AlertTriangle,
  Bot,
  CheckCircle2,
  Clock,
  FileDiff as FileDiffIcon,
  GitCommit,
  XCircle,
} from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import type { IssuePatchSubmission, PatchStatus } from '~/types/issue'
import { parsePatchFilesToFileDiffs, parseUnifiedDiffToFileDiffs } from '~/utils/patch'
import { relativeTime } from '~/utils/time'

const props = defineProps<{
  active: boolean
  owner: string
  name: string
  issueNumber: number
  canManage: boolean
  issueHeadSha: string
  issueBranch: string
}>()

const emit = defineEmits<{
  (e: 'applied', submissionId: number): void
  (e: 'rejected', submissionId: number): void
}>()

const { t } = useI18n()

// --- state ---
const patches = ref<IssuePatchSubmission[]>([])
const selectedId = ref<number | null>(null)
const selectedDetail = ref<IssuePatchSubmission | null>(null)
const listError = ref<string | null>(null)
const detailError = ref<string | null>(null)
const detailLoading = ref(false)

const applyBusy = ref(false)
const actionError = ref<string | null>(null)
const actionInfo = ref<string | null>(null)

// Reject dialog
const showReject = ref(false)
const rejectReason = ref('')
const rejectBusy = ref(false)

// --- derived ---
const selected = computed(() =>
  patches.value.find((p) => p.id === selectedId.value) ?? null,
)

// --- data loading ---
async function loadPatches() {
  listError.value = null
  try {
    const data = await $fetch<{ patches: IssuePatchSubmission[] }>(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/patches`,
      { credentials: 'include' },
    )
    patches.value = data?.patches ?? []
    // auto-select first if nothing selected or selected patch disappeared
    if (selectedId.value && !patches.value.find((p) => p.id === selectedId.value)) {
      selectedId.value = null
      selectedDetail.value = null
    }
    if (!selectedId.value && patches.value.length > 0) {
      selectedId.value = patches.value[0]!.id
      loadDetail(patches.value[0]!.id)
    }
  } catch (e: any) {
    listError.value = e?.data?.error ?? t('issue.patches.listLoadFailed')
    patches.value = []
  }
}

async function loadDetail(id: number) {
  detailError.value = null
  detailLoading.value = true
  try {
    const data = await $fetch<IssuePatchSubmission>(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/patches/${id}`,
      { credentials: 'include' },
    )
    // New API returns patches[] (patch files series). Prefer it over the
    // transitional patch_text single-diff field.
    if (data.patches && data.patches.length > 0) {
      data.files = parsePatchFilesToFileDiffs(data.patches)
    } else if (data.patch_text) {
      data.files = parseUnifiedDiffToFileDiffs(data.patch_text)
    }
    selectedDetail.value = data
  } catch (e: any) {
    detailError.value = e?.data?.error ?? t('issue.patches.detailLoadFailed')
    selectedDetail.value = null
  } finally {
    detailLoading.value = false
  }
}

function selectPatch(id: number) {
  selectedId.value = id
  actionError.value = null
  actionInfo.value = null
  loadDetail(id)
}

// --- actions ---
async function applyPatch() {
  const p = selectedDetail.value
  if (!p) return
  if (!confirm(t('issue.patches.confirmApply', { title: p.title, branch: props.issueBranch }))) return

  applyBusy.value = true
  actionError.value = null
  actionInfo.value = null
  try {
    await $fetch(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/patches/${p.id}/apply`,
      { method: 'POST', credentials: 'include' },
    )
    actionInfo.value = t('issue.patches.applyRequested')
    emit('applied', p.id)
    await loadPatches()
    // reload detail of the same patch (now 'applied')
    if (selectedId.value === p.id) {
      await loadDetail(p.id)
    }
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.patches.applyFailed')
  } finally {
    applyBusy.value = false
  }
}

async function rejectPatch() {
  const p = selectedDetail.value
  if (!p) return
  rejectBusy.value = true
  actionError.value = null
  try {
    await $fetch(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/patches/${p.id}/reject`,
      {
        method: 'POST',
        credentials: 'include',
        body: { reason: rejectReason.value.trim() || undefined },
      },
    )
    showReject.value = false
    rejectReason.value = ''
    emit('rejected', p.id)
    await loadPatches()
    if (selectedId.value === p.id) {
      await loadDetail(p.id)
    }
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.patches.rejectFailed')
  } finally {
    rejectBusy.value = false
  }
}

function openReject() {
  rejectReason.value = ''
  showReject.value = true
}

// --- polling ---
const REFRESH_MS = 5_000
let timer: ReturnType<typeof setInterval> | null = null

function startPoll() {
  if (timer || typeof window === 'undefined') return
  timer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    loadPatches()
  }, REFRESH_MS)
}

function stopPoll() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(
  () => props.active,
  (v) => {
    if (v) {
      loadPatches()
      startPoll()
    } else {
      stopPoll()
    }
  },
  { immediate: true },
)

onUnmounted(() => stopPoll())

// --- helpers ---
function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}
function shortSha(s: string) {
  return s ? s.slice(0, 7) : ''
}

function statusVariant(s: PatchStatus) {
  switch (s) {
    case 'submitted':
      return 'secondary' as const
    case 'applying':
      return 'outline' as const
    case 'applied':
      return 'default' as const
    case 'rejected':
      return 'destructive' as const
    case 'superseded':
      return 'outline' as const
    case 'withdrawn':
      return 'outline' as const
    case 'voided':
      return 'destructive' as const
  }
}

function statusIcon(s: PatchStatus) {
  switch (s) {
    case 'submitted':
      return Clock
    case 'applying':
      return Clock
    case 'applied':
      return CheckCircle2
    case 'rejected':
      return XCircle
    case 'superseded':
      return XCircle
    case 'withdrawn':
      return XCircle
    case 'voided':
      return XCircle
  }
}

// Informational: the patch was generated against an older head. This no
// longer blocks apply — the backend decides via real git-am conflict check.
const baseMismatch = computed(
  () =>
    selectedDetail.value?.status === 'submitted' &&
    selectedDetail.value.base_head_sha !== props.issueHeadSha,
)

const canApply = computed(() => {
  if (!props.canManage) return false
  const p = selectedDetail.value
  if (!p) return false
  // Only submitted patches can be applied. base_head_sha mismatch is
  // informational — the backend will check real conflicts via git am.
  return p.status === 'submitted'
})
</script>

<template>
  <div class="grid gap-4 lg:grid-cols-[280px_minmax(0,1fr)]">
    <!-- Left: patch list -->
    <div class="space-y-2">
      <p v-if="listError" class="text-sm text-destructive">{{ listError }}</p>

      <Card v-if="patches.length === 0" class="gap-0 py-0">
        <CardContent class="space-y-2 p-4 text-sm text-muted-foreground">
          <p>{{ t('issue.patches.empty') }}</p>
          <p class="text-xs">{{ t('issue.patches.emptyHint') }}</p>
        </CardContent>
      </Card>

      <div v-else class="max-h-96 space-y-1 overflow-y-auto lg:max-h-none">
        <button
          v-for="p in patches"
          :key="p.id"
          type="button"
          class="w-full rounded-lg border px-3 py-2.5 text-left transition-colors"
          :class="
            selectedId === p.id
              ? 'border-primary/50 bg-primary/5'
              : 'border-transparent hover:bg-muted/50'
          "
          @click="selectPatch(p.id)"
        >
          <div class="flex items-center gap-1.5">
            <component :is="statusIcon(p.status)" class="size-3.5 shrink-0 text-muted-foreground" />
            <span class="min-w-0 flex-1 truncate text-sm font-medium">{{ p.title }}</span>
          </div>
          <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
            <span class="flex items-center gap-1">
              <Bot class="size-3" />
              {{ p.agent_role }}
            </span>
            <Badge :variant="statusVariant(p.status)" class="px-1.5 py-0 text-[10px] leading-none">
              {{ t(`issue.patches.status.${p.status}`) }}
            </Badge>
            <span v-if="p.patch_count && p.patch_count > 1" class="flex items-center gap-1">
              <FileDiffIcon class="size-3" />
              {{ p.patch_count }}
            </span>
            <span class="font-mono">
              +{{ p.additions }}<span class="text-red-500">−{{ p.deletions }}</span>
            </span>
          </div>
          <div class="mt-0.5 text-xs text-muted-foreground">
            {{ rel(p.created_at) }}
          </div>
        </button>
      </div>
    </div>

    <!-- Right: detail -->
    <div class="min-w-0 space-y-4">
      <!-- Loading -->
      <Card v-if="detailLoading" class="gap-0 py-0">
        <CardContent class="p-4 text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </CardContent>
      </Card>

      <!-- Error -->
      <p v-else-if="detailError" class="text-sm text-destructive">{{ detailError }}</p>

      <!-- No selection -->
      <Card v-else-if="!selectedDetail" class="gap-0 py-0">
        <CardContent class="p-4 text-sm text-muted-foreground">
          {{ patches.length === 0 ? t('issue.patches.empty') : t('issue.patches.detailNotFound') }}
        </CardContent>
      </Card>

      <!-- Detail -->
      <template v-else>
        <!-- Action feedback -->
        <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>
        <p v-if="actionInfo" class="text-sm text-emerald-700 dark:text-emerald-400">
          {{ actionInfo }}
        </p>

        <!-- Metadata card -->
        <Card class="gap-0 py-0">
          <CardContent class="space-y-3 p-4">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h3 class="text-base font-semibold">{{ selectedDetail.title }}</h3>
              <Badge :variant="statusVariant(selectedDetail.status)">
                <component :is="statusIcon(selectedDetail.status)" class="mr-1 size-3" />
                {{ t(`issue.patches.status.${selectedDetail.status}`) }}
              </Badge>
            </div>

            <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span class="flex items-center gap-1">
                <Bot class="size-3" />
                {{ t('issue.patches.submittedBy', { role: selectedDetail.agent_role }) }}
              </span>
              <span>{{ rel(selectedDetail.created_at) }}</span>
              <span>{{ t('issue.patches.stats', { files: selectedDetail.file_count, additions: selectedDetail.additions, deletions: selectedDetail.deletions }) }}</span>
              <span class="font-mono">{{ t('issue.patches.basedOn', { sha: shortSha(selectedDetail.base_head_sha) }) }}</span>
            </div>

            <div v-if="selectedDetail.applied_commit_sha" class="flex items-center gap-1 text-xs text-muted-foreground">
              <GitCommit class="size-3" />
              <code class="font-mono">{{ shortSha(selectedDetail.applied_commit_sha) }}</code>
            </div>

            <div v-if="selectedDetail.rejected_reason" class="rounded bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {{ selectedDetail.rejected_reason }}
            </div>

            <!-- Base mismatch informational warning (no longer blocks apply) -->
            <div
              v-if="baseMismatch"
              class="rounded bg-amber-500/10 px-3 py-2 text-sm text-amber-700 dark:text-amber-300"
            >
              <AlertTriangle class="mr-1 inline size-4" />
              {{ t('issue.patches.baseMismatchHint', { baseSha: shortSha(selectedDetail.base_head_sha), headSha: shortSha(issueHeadSha) }) }}
            </div>

            <!-- Patch files sequence -->
            <div
              v-if="selectedDetail.patches && selectedDetail.patches.length > 0"
              class="space-y-1"
            >
              <p class="text-xs font-medium text-muted-foreground">
                {{ t('issue.patches.patchFiles', { count: selectedDetail.patches.length }) }}
              </p>
              <ol class="list-inside list-decimal space-y-0.5 pl-1 text-xs text-muted-foreground">
                <li v-for="pf in selectedDetail.patches" :key="pf.index" class="truncate font-mono">
                  {{ pf.file_name }}
                  <span v-if="pf.subject" class="ml-1 text-muted-foreground/60">
                    — {{ pf.subject }}
                  </span>
                </li>
              </ol>
            </div>

            <!-- Description -->
            <div v-if="selectedDetail.description" class="rounded bg-muted/40 px-3 py-2 text-sm">
              {{ selectedDetail.description }}
            </div>
            <p v-else class="text-xs text-muted-foreground">
              {{ t('issue.patches.noDescription') }}
            </p>

            <!-- Actions: Apply / Reject (maintainer only) -->
            <div v-if="canManage" class="flex gap-2 border-t pt-3">
              <Button
                size="sm"
                :disabled="applyBusy || !canApply"
                @click="applyPatch"
              >
                <CheckCircle2 class="size-4" />
                {{ applyBusy ? t('issue.patches.applying') : t('issue.patches.apply') }}
              </Button>
              <Button
                v-if="selectedDetail.status === 'submitted' || selectedDetail.status === 'applying'"
                size="sm"
                variant="outline"
                :disabled="rejectBusy"
                @click="openReject"
              >
                <XCircle class="size-4" />
                {{ t('issue.patches.reject') }}
              </Button>
            </div>
          </CardContent>
        </Card>

        <!-- Reject dialog (inline card) -->
        <Card v-if="showReject" class="gap-0 py-0 border-destructive/50">
          <CardContent class="space-y-3 p-4">
            <label class="block text-sm font-medium">
              {{ t('issue.patches.rejectReason') }}
            </label>
            <textarea
              v-model="rejectReason"
              rows="3"
              class="w-full rounded-md border bg-background px-3 py-2 text-sm"
              :placeholder="t('issue.patches.rejectReasonPlaceholder')"
            />
            <div class="flex gap-2">
              <Button
                size="sm"
                variant="destructive"
                :disabled="rejectBusy"
                @click="rejectPatch"
              >
                {{ rejectBusy ? t('issue.patches.rejecting') : t('issue.patches.rejectSubmit') }}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                @click="showReject = false"
              >
                {{ t('common.cancel') }}
              </Button>
            </div>
          </CardContent>
        </Card>

        <!-- Diff -->
        <FileDiffList
          v-if="selectedDetail.files && selectedDetail.files.length > 0"
          :diffs="selectedDetail.files"
          :owner="owner"
          :name="name"
        />
      </template>
    </div>
  </div>
</template>
