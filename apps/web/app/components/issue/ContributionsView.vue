<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import {
  Bot,
  Check,
  CheckCircle2,
  CircleDot,
  GitMerge,
  Lock,
  MessageSquare,
  MinusCircle,
  ThumbsDown,
  ThumbsUp,
  X,
  XCircle,
} from 'lucide-vue-next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import type {
  Contribution,
  ContributionStatus,
  IssueComment,
  ReviewStatus,
  ReviewVerdict,
  ReviewVoteValue,
} from '~/types/issue'
import type { FileDiff } from '~/types/repo'
import { relativeTime } from '~/utils/time'

const props = defineProps<{
  active: boolean
  owner: string
  name: string
  issueNumber: number
  canManage: boolean
}>()

const emit = defineEmits<{
  (e: 'applied', contributionId: number): void
  (e: 'closed', contributionId: number): void
}>()

const { t } = useI18n()

// --- state ---
const contributions = ref<Contribution[]>([])
const selectedId = ref<number | null>(null)
const detail = ref<{
  contribution: Contribution
  diff: FileDiff[]
  review: ReviewStatus | null
  comments: IssueComment[]
} | null>(null)
const listError = ref<string | null>(null)
const detailError = ref<string | null>(null)
const detailLoading = ref(false)

const applyBusy = ref(false)
const closeBusy = ref(false)
const actionError = ref<string | null>(null)
const actionInfo = ref<string | null>(null)

const detailTab = ref<'diff' | 'reviews' | 'comments' | 'checks'>('diff')

// --- data loading ---
async function loadList() {
  listError.value = null
  try {
    const data = await $fetch<{ contributions: Contribution[] }>(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/contributions?include_merged=true&include_closed=true`,
      { credentials: 'include' },
    )
    contributions.value = data?.contributions ?? []
    // Auto-select first when nothing selected, or clear a vanished selection.
    if (selectedId.value && !contributions.value.find((c) => c.id === selectedId.value)) {
      selectedId.value = null
      detail.value = null
    }
    if (!selectedId.value && contributions.value.length > 0) {
      selectedId.value = contributions.value[0]!.id
      loadDetail(contributions.value[0]!.id)
    }
  } catch (e: any) {
    listError.value = e?.data?.error ?? t('issue.contributions.listLoadFailed')
    contributions.value = []
  }
}

async function loadDetail(id: number) {
  detailError.value = null
  detailLoading.value = true
  try {
    const data = await $fetch<{
      contribution: Contribution
      diff: FileDiff[]
      review: ReviewStatus | null
      comments: IssueComment[]
    }>(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/contributions/${id}`,
      { credentials: 'include' },
    )
    detail.value = {
      contribution: data.contribution,
      diff: data.diff ?? [],
      review: data.review ?? null,
      comments: data.comments ?? [],
    }
  } catch (e: any) {
    detailError.value = e?.data?.error ?? t('issue.contributions.detailLoadFailed')
    detail.value = null
  } finally {
    detailLoading.value = false
  }
}

function selectContribution(id: number) {
  selectedId.value = id
  actionError.value = null
  actionInfo.value = null
  detailTab.value = 'diff'
  loadDetail(id)
}

// --- actions ---
async function applyContribution() {
  const c = detail.value?.contribution
  if (!c) return
  if (!confirm(t('issue.contributions.confirmApply', { title: c.title || c.ref_name }))) return

  applyBusy.value = true
  actionError.value = null
  actionInfo.value = null
  try {
    const res = await $fetch<{ merge_sha: string, mode: string }>(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/contributions/${c.id}/apply`,
      { method: 'POST', credentials: 'include' },
    )
    actionInfo.value = t('issue.contributions.applyOk', { sha: shortSha(res.merge_sha), mode: res.mode })
    emit('applied', c.id)
    await loadList()
    if (selectedId.value === c.id) await loadDetail(c.id)
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.contributions.applyFailed')
  } finally {
    applyBusy.value = false
  }
}

async function closeContribution() {
  const c = detail.value?.contribution
  if (!c) return
  if (!confirm(t('issue.contributions.confirmClose', { title: c.title || c.ref_name }))) return

  closeBusy.value = true
  actionError.value = null
  actionInfo.value = null
  try {
    await $fetch(
      `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/contributions/${c.id}/close`,
      { method: 'POST', credentials: 'include' },
    )
    emit('closed', c.id)
    await loadList()
    if (selectedId.value === c.id) await loadDetail(c.id)
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.contributions.closeFailed')
  } finally {
    closeBusy.value = false
  }
}

// --- polling ---
const REFRESH_MS = 5_000
let timer: ReturnType<typeof setInterval> | null = null

function startPoll() {
  if (timer || typeof window === 'undefined') return
  timer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    loadList()
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
      loadList()
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
function initialOf(s?: string) {
  return s ? s.charAt(0).toUpperCase() : '?'
}

function statusVariant(s: ContributionStatus) {
  switch (s) {
    case 'pending':
      return 'secondary' as const
    case 'approved':
      return 'default' as const
    case 'rejected':
      return 'destructive' as const
    case 'merged':
      return 'default' as const
    case 'closed':
      return 'outline' as const
  }
}

function statusIcon(s: ContributionStatus) {
  switch (s) {
    case 'pending':
      return CircleDot
    case 'approved':
      return ThumbsUp
    case 'rejected':
      return ThumbsDown
    case 'merged':
      return GitMerge
    case 'closed':
      return Lock
  }
}

// A contribution is shown as conflicting when the server marked it
// non-mergeable or the merge_mode is the explicit 'conflicted' sentinel.
function isConflict(c: Contribution) {
  return !c.mergeable || c.merge_mode === 'conflicted'
}

// Terminal states (merged / closed) get muted styling to visually
// separate them from actionable pending / approved items.
function isTerminal(s: ContributionStatus) {
  return s === 'merged' || s === 'closed'
}

// --- review vote rendering (mirrors [number].vue right sidebar) ---
function voteValueClass(v: ReviewVoteValue) {
  switch (v) {
    case 'approve': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'reject': return 'bg-red-500/15 text-red-700 dark:text-red-300'
    case 'abstain': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}
function voteValueIcon(v: ReviewVoteValue) {
  switch (v) {
    case 'approve': return ThumbsUp
    case 'reject': return ThumbsDown
    case 'abstain': return MinusCircle
  }
}
function verdictClass(v: ReviewVerdict) {
  switch (v) {
    case 'approved': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'rejected': return 'bg-red-500/15 text-red-700 dark:text-red-300'
    case 'pending': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}

// --- action gates ---
// A contribution is actionable while it is still in review (`pending`) or has
// passed review and is ready to apply (`approved`). Branches are immutable, so
// `rejected` is terminal from the maintainer's side (the author must push a new
// versioned branch); `merged` / `closed` are likewise terminal.
const isActionable = computed(() => {
  const s = detail.value?.contribution.status
  return s === 'pending' || s === 'approved'
})

const canApply = computed(() => {
  if (!props.canManage) return false
  const c = detail.value?.contribution
  if (!c) return false
  return isActionable.value && c.mergeable && c.merge_mode !== 'conflicted'
})
</script>

<template>
  <div class="grid gap-4 lg:grid-cols-[280px_minmax(0,1fr)]">
    <!-- Left: contribution list -->
    <div class="space-y-2">
      <p v-if="listError" class="text-sm text-destructive">{{ listError }}</p>

      <Card v-if="contributions.length === 0" class="gap-0 py-0">
        <CardContent class="space-y-2 p-4 text-sm text-muted-foreground">
          <p>{{ t('issue.contributions.empty') }}</p>
          <p class="text-xs">{{ t('issue.contributions.emptyHint') }}</p>
        </CardContent>
      </Card>

      <div v-else class="max-h-96 space-y-1 overflow-y-auto lg:max-h-none">
        <button
          v-for="c in contributions"
          :key="c.id"
          type="button"
          class="w-full rounded-lg border px-3 py-2.5 text-left transition-colors"
          :class="[
            selectedId === c.id
              ? 'border-primary/50 bg-primary/5'
              : 'border-transparent hover:bg-muted/50',
            isTerminal(c.status) ? 'opacity-60 bg-muted/10' : '',
          ]"
          @click="selectContribution(c.id)"
        >
          <div class="flex items-center gap-1.5">
            <component :is="statusIcon(c.status)" class="size-3.5 shrink-0 text-muted-foreground" />
            <span class="min-w-0 flex-1 truncate text-sm font-medium">
              {{ c.title || c.ref_name }}
            </span>
          </div>
          <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted-foreground">
            <span class="flex items-center gap-1">
              <Bot class="size-3" />
              {{ c.agent_role }}
            </span>
            <Badge :variant="statusVariant(c.status)" class="px-1.5 py-0 text-[10px] leading-none">
              {{ t(`issue.contributions.status.${c.status}`) }}
            </Badge>
            <span
              v-if="isConflict(c)"
              class="flex items-center gap-0.5 text-red-600 dark:text-red-400"
              :title="t('issue.contributions.conflict')"
            >
              <X class="size-3" />
              {{ t('issue.contributions.conflict') }}
            </span>
            <span
              v-else
              class="flex items-center gap-0.5 text-emerald-600 dark:text-emerald-400"
              :title="t('issue.contributions.mergeable')"
            >
              <Check class="size-3" />
              {{ t('issue.contributions.mergeable') }}
            </span>
          </div>
          <div class="mt-0.5 flex items-center gap-2 text-xs text-muted-foreground">
            <code class="font-mono">{{ shortSha(c.head_sha) }}</code>
            <span>{{ rel(c.created_at) }}</span>
          </div>
        </button>
      </div>
    </div>

    <!-- Right: detail -->
    <div class="min-w-0 space-y-4">
      <Card v-if="detailLoading" class="gap-0 py-0">
        <CardContent class="p-4 text-sm text-muted-foreground">
          {{ t('common.loading') }}
        </CardContent>
      </Card>

      <p v-else-if="detailError" class="text-sm text-destructive">{{ detailError }}</p>

      <Card v-else-if="!detail" class="gap-0 py-0">
        <CardContent class="p-4 text-sm text-muted-foreground">
          {{ contributions.length === 0 ? t('issue.contributions.empty') : t('issue.contributions.detailNotFound') }}
        </CardContent>
      </Card>

      <template v-else>
        <!-- Action feedback -->
        <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>
        <p v-if="actionInfo" class="text-sm text-emerald-700 dark:text-emerald-400">
          {{ actionInfo }}
        </p>

        <!-- Header -->
        <Card class="gap-0 py-0">
          <CardContent class="space-y-3 p-4">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h3 class="min-w-0 truncate text-base font-semibold">
                {{ detail.contribution.title || detail.contribution.ref_name }}
              </h3>
              <Badge :variant="statusVariant(detail.contribution.status)">
                <component :is="statusIcon(detail.contribution.status)" class="mr-1 size-3" />
                {{ t(`issue.contributions.status.${detail.contribution.status}`) }}
              </Badge>
            </div>

            <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span class="flex items-center gap-1">
                <Bot class="size-3" />
                {{ t('issue.contributions.role', { role: detail.contribution.agent_role }) }}
              </span>
              <code class="font-mono">{{ detail.contribution.ref_name }}</code>
              <span class="font-mono">
                <span class="text-emerald-600 dark:text-emerald-400">+{{ detail.contribution.additions }}</span>
                <span class="ml-1 text-red-600 dark:text-red-400">−{{ detail.contribution.deletions }}</span>
              </span>
              <span class="font-mono">
                {{ t('issue.contributions.headSha', { sha: shortSha(detail.contribution.head_sha) }) }}
              </span>
              <span class="font-mono">
                {{ t('issue.contributions.basedOn', { sha: shortSha(detail.contribution.base_sha) }) }}
              </span>
            </div>

            <div
              v-if="isConflict(detail.contribution) && isActionable"
              class="flex items-center gap-1 rounded bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300"
            >
              <XCircle class="size-4" />
              {{ t('issue.contributions.conflict') }}
            </div>

            <div v-if="detail.contribution.merged_commit_sha" class="flex items-center gap-1 text-xs text-muted-foreground">
              <GitMerge class="size-3" />
              <code class="font-mono">{{ shortSha(detail.contribution.merged_commit_sha) }}</code>
            </div>

            <div v-if="detail.contribution.description" class="rounded bg-muted/40 px-3 py-2 text-sm">
              <MarkdownBody :source="detail.contribution.description" />
            </div>

            <!-- Actions: Apply / Close -->
            <div v-if="isActionable" class="flex gap-2 border-t pt-3">
              <Button
                v-if="canManage"
                size="sm"
                :disabled="applyBusy || !canApply"
                @click="applyContribution"
              >
                <CheckCircle2 class="size-4" />
                {{ applyBusy ? t('issue.contributions.applying') : t('issue.contributions.apply') }}
              </Button>
              <Button
                size="sm"
                variant="outline"
                :disabled="closeBusy"
                @click="closeContribution"
              >
                <XCircle class="size-4" />
                {{ closeBusy ? t('issue.contributions.closing') : t('issue.contributions.close') }}
              </Button>
            </div>
          </CardContent>
        </Card>

        <!-- Tabbed detail panel -->
        <Tabs v-model="detailTab">
          <TabsList>
            <TabsTrigger value="diff">{{ t('issue.contributions.tabs.diff') }}</TabsTrigger>
            <TabsTrigger value="reviews">{{ t('issue.contributions.tabs.reviews') }}</TabsTrigger>
            <TabsTrigger value="comments">{{ t('issue.contributions.tabs.comments') }}</TabsTrigger>
            <TabsTrigger value="checks">{{ t('issue.contributions.tabs.checks') }}</TabsTrigger>
          </TabsList>

          <!-- Diff -->
          <TabsContent value="diff" class="mt-3 space-y-3">
            <Card v-if="detail.diff.length === 0" class="gap-0 py-0">
              <CardContent class="p-4 text-sm text-muted-foreground">
                {{ t('issue.contributions.empty') }}
              </CardContent>
            </Card>
            <div
              v-else
              data-contributions-diff-scroll
              class="overflow-y-auto max-h-[60vh]"
            >
              <FileDiffList
                :diffs="detail.diff"
                :owner="owner"
                :name="name"
                :ref-before="detail.contribution.base_sha"
                :ref-after="detail.contribution.head_sha"
              />
            </div>
          </TabsContent>

          <!-- Reviews -->
          <TabsContent value="reviews" class="mt-3">
            <Card class="gap-0 py-0">
              <CardContent class="space-y-3 p-4">
                <div class="flex items-center justify-between gap-2">
                  <p class="flex items-center gap-1 text-xs text-muted-foreground">
                    <ThumbsUp class="size-3" />
                    {{ t('issue.review.title') }}
                  </p>
                  <Badge
                    :class="verdictClass(detail.review?.verdict ?? 'pending')"
                    variant="secondary"
                  >
                    {{ t(`issue.review.verdict.${detail.review?.verdict ?? 'pending'}`) }}
                  </Badge>
                </div>

                <p
                  v-if="detail.review?.head_sha"
                  class="text-xs text-muted-foreground"
                >
                  {{ t('issue.review.basedOn', { sha: shortSha(detail.review.head_sha) }) }}
                </p>

                <p
                  v-if="!detail.review || detail.review.votes.length === 0"
                  class="text-xs text-muted-foreground"
                >
                  {{ t('issue.review.empty') }}
                </p>
                <ul v-else class="space-y-1.5">
                  <li
                    v-for="v in detail.review.votes"
                    :key="v.reviewer"
                    class="flex items-center gap-2 text-xs"
                  >
                    <Bot class="size-3 shrink-0 text-muted-foreground" />
                    <span
                      class="min-w-0 flex-1 truncate font-medium text-foreground"
                      :title="`@agent-${v.reviewer}`"
                    >
                      @agent-{{ v.reviewer }}
                    </span>
                    <Badge :class="voteValueClass(v.value)" variant="secondary" class="shrink-0">
                      <component :is="voteValueIcon(v.value)" class="mr-1 size-3" />
                      {{ t(`issue.review.vote.${v.value}`) }}
                    </Badge>
                  </li>
                </ul>

                <p
                  v-if="detail.review?.stale_votes?.length"
                  class="text-xs text-amber-600 dark:text-amber-400"
                >
                  {{ t('issue.review.staleWarning', { n: detail.review.stale_votes.length }) }}
                </p>
              </CardContent>
            </Card>
          </TabsContent>

          <!-- Comments -->
          <TabsContent value="comments" class="mt-3 space-y-3">
            <Card v-if="detail.comments.length === 0" class="gap-0 py-0">
              <CardContent class="p-4 text-sm text-muted-foreground">
                {{ t('issue.contributions.commentsEmpty') }}
              </CardContent>
            </Card>
            <Card
              v-for="cm in detail.comments"
              v-else
              :key="cm.id"
              class="gap-0 py-0"
            >
              <CardContent class="p-0">
                <div class="flex flex-wrap items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                  <Avatar class="size-6 shrink-0">
                    <AvatarFallback class="bg-primary/10 text-[10px] text-primary">
                      <Bot v-if="cm.agent_role" class="size-3" />
                      <template v-else>{{ initialOf(cm.author_username) }}</template>
                    </AvatarFallback>
                  </Avatar>
                  <span class="font-medium text-foreground">
                    {{ cm.agent_role ? `@agent-${cm.agent_role}` : (cm.author_username || '—') }}
                  </span>
                  <code v-if="cm.file_path" class="font-mono text-muted-foreground">
                    {{ cm.file_path }}<span v-if="cm.line">:{{ cm.line }}</span>
                  </code>
                  <span class="text-muted-foreground">· {{ rel(cm.created_at) }}</span>
                </div>
                <div class="px-4 py-3 text-sm">
                  <MarkdownBody :source="cm.body" />
                </div>
              </CardContent>
            </Card>
          </TabsContent>

          <!-- Checks (stub, M8) -->
          <TabsContent value="checks" class="mt-3">
            <Card class="gap-0 py-0">
              <CardContent class="flex items-center gap-2 p-4 text-sm text-muted-foreground">
                <MessageSquare class="size-4" />
                {{ t('issue.contributions.checksPlaceholder') }}
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>
      </template>
    </div>
  </div>
</template>
