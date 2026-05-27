<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch, nextTick } from 'vue'
import {
  ArrowUp,
  Bot,
  Circle,
  CircleCheck,
  CircleDot,
  CircleSlash,
  CornerDownRight,
  Diff as DiffIcon,
  FileDiff as FileDiffIcon,
  GitBranch,
  GitCommit,
  GitMerge,
  LayoutGrid,
  ListTodo,
  Lock,
  Play,
  MessageSquare,
  MinusCircle,
  Plus,
  ThumbsDown,
  ThumbsUp,
} from 'lucide-vue-next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import ActorBadge from '@/components/ActorBadge.vue'
import AgentSessionsView from '@/components/issue/AgentSessionsView.vue'
import CheckRunPanel from '@/components/issue/CheckRunPanel.vue'
import ContributionsView from '@/components/issue/ContributionsView.vue'
import PlanView from '@/components/issue/PlanView.vue'
import QuestionnaireTimelineCard from '@/components/issue/QuestionnaireTimelineCard.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import FileDiffList from '@/components/repo/FileDiffList.vue'
import FoldableBody from '@/components/issue/FoldableBody.vue'
import MentionTextarea from '@/components/issue/MentionTextarea.vue'
import AttachmentUploader from '@/components/issue/AttachmentUploader.vue'
import type { Issue, IssueState, IssueTimeline, IssueMergeResp, OpenDescendant, ReviewStatus, ReviewVerdict, ReviewVoteValue, TodoStatus } from '~/types/issue'
import type { ActorRef } from '~/types/actor'
import type { Commit, FileDiff } from '~/types/repo'
import { useQuestionnaire } from '@/composables/useQuestionnaire'
import { useWindowScroll, useWindowSize } from '@vueuse/core'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const number = computed(() => Number(route.params.number ?? 0))
const issue = ref<Issue | null>(null)

// Dynamic header height tracking for sticky sidebar positioning.
// When the issue title wraps the header grows beyond the default
// 8rem (top-32), so we measure it with a ResizeObserver instead of
// hardcoding a fixed offset.
const headerRef = ref<HTMLElement | null>(null)
const headerHeight = ref(128) // default 8rem fallback (Tailwind top-32)
let _headerObserver: ResizeObserver | null = null

// Dynamic sidebar height tracking for sticky-bottom anchoring.
// When the sidebar is taller than the viewport we pin its bottom
// edge instead of its top, so Merge/Close are always reachable.
const asideRef = ref<HTMLElement | null>(null)
const asideHeight = ref(0)
let _asideObserver: ResizeObserver | null = null

// Pin the sidebar's top edge: classic sticky-top when it fits
// below the header; negative offset (sticky-bottom) when taller
// than the viewport. Math.min picks the binding constraint.
const stickyTop = computed(() =>
  Math.min(headerHeight.value, viewportHeight.value - asideHeight.value),
)

// Scroll-to-top button
const { y: scrollY } = useWindowScroll()
const { height: viewportHeight } = useWindowSize()
const showScrollTop = computed(() => scrollY.value > 480)
function scrollToTop() {
  window.scrollTo({ top: 0, behavior: 'smooth' })
}


useHead({ title: () => {
    const issueTitle = issue.value?.title
    return issueTitle
      ? `${issueTitle} · #${number.value} · ${owner.value}/${name.value} - ${t('app.name')}`
      : `#${number.value} · ${owner.value}/${name.value} - ${t('app.name')}`
  } })

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.issues'), to: `${base}/issues` },
    { label: `#${number.value}` },
  ]
})

const { repo, load: loadRepo } = useRepo(() => owner.value, () => name.value)
const issueError = ref<string | null>(null)
const timeline = ref<IssueTimeline | null>(null)
const diff = ref<FileDiff[]>([])
const diffError = ref<string | null>(null)
const commits = ref<Commit[]>([])
const parent = ref<Issue | null>(null)
const children = ref<Issue[]>([])

// Questionnaire composable — loads the list of questionnaires for this issue
const {
  questionnaires,
  load: loadQuestionnaires,
} = useQuestionnaire(
  () => owner.value,
  () => name.value,
  () => number.value,
)

const commentBody = ref('')
const commentBusy = ref(false)
const commentError = ref<string | null>(null)
const mentionTextareaRef = ref<InstanceType<typeof MentionTextarea> | null>(null)



// Mention suggestions for the comment editor's `@` autocomplete. The
// list comes from the host yaml at the default-branch tip, which can
// change mid-page (a merge that touches `.hangrix/agents.yml` adds /
// removes roles), so we refresh it from the same poll that refreshes
// the issue body and right after a merge — otherwise newly-added
// roles wouldn't show up in the dropdown until the user reloads.
interface MentionAgent { role_key: string }
const mentionAgents = ref<MentionAgent[]>([])
// hostYamlError surfaces a parse error in `.hangrix/agents.yml` at the
// default-branch tip. When set, the dropdown is empty AND `issue.opened`
// / `issue.comment` triggers also no-op server-side — without this
// signal the operator has no clue why agents stopped responding after a
// merge that touched the file.
const hostYamlError = ref<string | null>(null)
async function loadMentionAgents() {
  try {
    const res = await $fetch<{ agents: MentionAgent[], host_yaml_error?: string }>(
      `/api/repos/${owner.value}/${name.value}/mention-suggestions`,
      { credentials: 'include' },
    )
    mentionAgents.value = res.agents ?? []
    hostYamlError.value = res.host_yaml_error ? res.host_yaml_error : null
  } catch {
    mentionAgents.value = []
    hostYamlError.value = null
  }
}

const stateBusy = ref(false)
const mergeBusy = ref(false)
const actionError = ref<string | null>(null)
const actionInfo = ref<string | null>(null)
const actionSubIssues = ref<OpenDescendant[]>([])

type IssueTab = 'conversation' | 'commits' | 'diff' | 'contributions' | 'agents' | 'plan'

// tab state is mirrored into ?tab= so the URL is shareable / refresh-stable.
// `conversation` is the implicit default — we drop the query key entirely
// when it's selected so deep links to "/issues/N" stay clean.
function parseTab(raw: unknown): IssueTab {
  if (raw === 'commits' || raw === 'diff' || raw === 'contributions' || raw === 'agents' || raw === 'plan') return raw
  return 'conversation'
}
const tab = ref<IssueTab>(parseTab(route.query.tab))

watch(tab, (v) => {
  router.replace({
    query: { ...route.query, tab: v === 'conversation' ? undefined : v },
  })
})

watch(() => route.query.tab, (q) => {
  const next = parseTab(q)
  if (next !== tab.value) tab.value = next
})

const canManage = computed(() => {
  if (!repo.value || !user.value) return false
  return user.value.role === 'admin' || user.value.id === repo.value.owner_id
})

const canMerge = computed(() => {
  return canManage.value && issue.value?.state === 'open' && Boolean(issue.value?.head_sha)
})

// Review gate properties — driven by the server-computed review_status, not
// by client-side timeline derivation. When review_status is absent (old
// backend) we default to "not blocked" for backward compatibility.
const reviewStatus = computed<ReviewStatus | null>(() => issue.value?.review_status ?? null)

// Todos — driven by the server-embedded todos + todo_summary on the issue
// response. Absent means the backend doesn't support todos yet.
const todos = computed(() => issue.value?.todos ?? [])
const todoSummary = computed(() => issue.value?.todo_summary ?? null)

const mergeBlocked = computed(() => reviewStatus.value?.merge_blocked ?? false)

// Localized block reason for display. Falls back to a generic message when
// the reason code is unrecognised.
const mergeBlockReason = computed(() => {
  if (!mergeBlocked.value) return ''
  const reason = reviewStatus.value?.block_reason
  if (reason === 'review_required') {
    return t(`issue.review.blockReason.${reason}`)
  }
  return t('issue.review.blocked')
})

// Sub-issue gate: when any direct child is open, disable Merge/Close as a
// client-side hint. The server is the authoritative gate — it also catches
// deep descendants that the client-side check can't see.
const openSubIssues = computed(() => children.value.filter(c => c.state === 'open'))
const subIssueBlocked = computed(() => openSubIssues.value.length > 0)
const subIssueBlockMessage = computed(() => {
  if (openSubIssues.value.length === 0) return ''
  return t('issue.subIssueBlock', { n: openSubIssues.value.length })
})


// Aggregate +/- across every file in the issue diff. Parses each unified
// patch once and counts `+`/`-` lines (not the file-header `+++`/`---`).
// The result feeds the "Changes" card so users see the issue's footprint
// without having to expand individual files.
const diffStats = computed(() => {
  let added = 0
  let deleted = 0
  let filesChanged = 0
  for (const f of diff.value) {
    if (f.binary) {
      filesChanged++
      continue
    }
    let touched = false
    for (const line of f.patch.split('\n')) {
      if (line.startsWith('+++') || line.startsWith('---')) continue
      if (line.startsWith('+')) { added++; touched = true }
      else if (line.startsWith('-')) { deleted++; touched = true }
    }
    if (touched || f.status !== 'modified') filesChanged++
  }
  return { added, deleted, filesChanged }
})

async function loadIssue() {
  issueError.value = null
  try {
    issue.value = await $fetch<Issue>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}`,
      { credentials: 'include' },
    )
  } catch (e: any) {
    issueError.value = e?.data?.error ?? t('issue.listFailed')
    issue.value = null
  }
}

async function loadParent() {
  parent.value = null
  if (!issue.value || !issue.value.parent_number) return
  try {
    parent.value = await $fetch<Issue>(
      `/api/repos/${owner.value}/${name.value}/issues/${issue.value.parent_number}`,
      { credentials: 'include' },
    )
  } catch {
    parent.value = null
  }
}

async function loadChildren() {
  if (!issue.value) {
    children.value = []
    return
  }
  try {
    const res = await $fetch<Issue[]>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/children`,
      { credentials: 'include' },
    )
    children.value = res ?? []
  } catch {
    children.value = []
  }
}

async function loadTimeline() {
  try {
    timeline.value = await $fetch<IssueTimeline>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/timeline`,
      { credentials: 'include' },
    )
  } catch {
    timeline.value = { comments: [], events: [] }
  }
}

async function loadDiff() {
  diffError.value = null
  try {
    const data = await $fetch<FileDiff[]>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/diff`,
      { credentials: 'include' },
    )
    diff.value = data ?? []
  } catch (e: any) {
    diffError.value = e?.data?.error ?? t('issue.diffLoadFailed')
    diff.value = []
  }
}

async function loadCommits() {
  try {
    const data = await $fetch<Commit[]>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/commits`,
      { credentials: 'include' },
    )
    commits.value = data ?? []
  } catch {
    commits.value = []
  }
}

async function submitComment() {
  const body = commentBody.value.trim()
  if (!body || !issue.value) return
  commentBusy.value = true
  commentError.value = null
  try {
    await $fetch(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/comments`,
      {
        method: 'POST',
        credentials: 'include',
        body: { body },
      },
    )
    commentBody.value = ''
    await loadTimeline()
  } catch (e: any) {
    commentError.value = e?.data?.error ?? t('issue.commentForm.failed')
  } finally {
    commentBusy.value = false
  }
}

async function toggleState() {
  if (!issue.value) return
  stateBusy.value = true
  actionError.value = null
  actionSubIssues.value = []
  try {
    const next: IssueState = issue.value.state === 'open' ? 'closed' : 'open'
    issue.value = await $fetch<Issue>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}`,
      {
        method: 'PATCH',
        credentials: 'include',
        body: { state: next },
      },
    )
    await loadTimeline()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.stateChangeFailed')
    actionSubIssues.value = e?.data?.sub_issues ?? []
  } finally {
    stateBusy.value = false
  }
}

async function merge() {
  if (!issue.value || !repo.value) return
  if (!confirm(t('issue.mergeConfirm', { n: issue.value.number, base: issue.value.base_branch }))) return
  mergeBusy.value = true
  actionError.value = null
  actionInfo.value = null
  actionSubIssues.value = []
  try {
    const res = await $fetch<IssueMergeResp>(
      `/api/repos/${owner.value}/${name.value}/issues/${number.value}/merge`,
      { method: 'POST', credentials: 'include', body: {} },
    )
    issue.value = res.issue
    actionInfo.value = t('issue.mergeOk', { base: res.issue.base_branch, mode: res.mode })
    await Promise.all([loadTimeline(), loadDiff(), loadMentionAgents()])
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('issue.mergeFailed')
    actionSubIssues.value = e?.data?.sub_issues ?? []
  } finally {
    mergeBusy.value = false
  }
}

function rel(s?: string | null) { return relativeTime(s ?? null, t) }
function shortSha(s: string) { return s ? s.slice(0, 7) : '' }
function formatDate(s?: string | null) {
  if (!s) return ''
  try { return new Date(s).toLocaleString() } catch { return s }
}
function initialOf(s?: string) {
  return s ? s.charAt(0).toUpperCase() : '?'
}

// --- actor normalization helpers ---
// These derive an ActorRef from either the unified `actor` field (preferred)
// or fall back to the legacy author_id/author_username/agent_role fields.
// This keeps the template rendering unified while the backend migration
// is in progress.

function normalizeActor(
  actor: ActorRef | undefined | null,
  legacy: { author_id?: number | null; author_username?: string | null; agent_role?: string | null },
): ActorRef | null {
  if (actor) {
    // If the server sent an actor with an empty display_name (can happen
    // when the DB actor columns haven't been backfilled yet, or when the
    // backend's legacy fallback omits the author name), patch it from the
    // legacy fields so the ActorBadge has something to render.
    if (!actor.display_name) {
      if (legacy.agent_role) {
        return { ...actor, display_name: `@agent-${legacy.agent_role}`, role_key: legacy.agent_role }
      }
      if (legacy.author_username) {
        return { ...actor, display_name: legacy.author_username }
      }
    }
    return actor
  }
  // Fallback: reconstruct from legacy fields
  if (legacy.agent_role) {
    return {
      kind: 'agent',
      id: `agent:${legacy.agent_role}`,
      display_name: `@agent-${legacy.agent_role}`,
      role_key: legacy.agent_role,
    }
  }
  if (legacy.author_username) {
    return {
      kind: 'user',
      id: legacy.author_id ? `user:${legacy.author_id}` : `user:${legacy.author_username}`,
      display_name: legacy.author_username,
      user_id: legacy.author_id ?? undefined,
    }
  }
  // Both empty → system
  return {
    kind: 'system',
    id: 'system:server',
    display_name: 'System',
  }
}

function commentActor(c: any): ActorRef | null {
  return normalizeActor(c.actor, {
    author_id: c.author_id,
    author_username: c.author_username,
    agent_role: c.agent_role,
  })
}

function eventActor(e: any): ActorRef | null {
  return normalizeActor(e.actor, {
    author_id: e.actor_id,
    author_username: e.actor_username,
    agent_role: e.agent_role,
  })
}

function issueActor(issue: Issue): ActorRef | null {
  return normalizeActor(issue.actor, {
    author_id: issue.author_id,
    author_username: issue.author_username,
    agent_role: null,
  })
}

function stateBadgeClass(s: IssueState) {
  switch (s) {
    case 'open': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'merged': return 'bg-violet-500/15 text-violet-700 dark:text-violet-300'
    case 'closed': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}
function stateBadgeIcon(s: IssueState) {
  if (s === 'merged') return GitMerge
  if (s === 'closed') return Lock
  return CircleDot
}

// timelineItems flattens comments + events into a single chronological list
// the template can iterate without picking out the kinds. Each item carries
// a discriminator the renderer switches on.
interface TimelineItem {
  key: string
  at: string
  kind: 'comment' | 'event' | 'questionnaire'
  data: any
}
const timelineItems = computed<TimelineItem[]>(() => {
  const out: TimelineItem[] = []
  for (const c of timeline.value?.comments ?? []) {
    out.push({ key: `c-${c.id}`, at: c.created_at, kind: 'comment', data: c })
  }
  for (const e of timeline.value?.events ?? []) {
    // questionnaire_posted events are rendered from the questionnaire API
    // (QuestionnaireTimelineCard) — skip them here to avoid duplication.
    if (e.kind === 'questionnaire_posted') continue
    out.push({ key: `e-${e.id}`, at: e.created_at, kind: 'event', data: e })
  }
  for (const q of questionnaires.value ?? []) {
    out.push({ key: `q-${q.id}`, at: q.my_submission?.submitted_at ?? q.created_at, kind: 'questionnaire', data: q })
  }
  out.sort((a, b) => {
    const ta = Date.parse(a.at)
    const tb = Date.parse(b.at)
    if (Number.isNaN(ta) && Number.isNaN(tb)) return 0
    if (Number.isNaN(ta)) return 1
    if (Number.isNaN(tb)) return -1
    return ta - tb
  })
  return out
})

function eventLabel(e: any): string {
  const actor = eventActor(e)
  const name = actor?.display_name || '—'
  switch (e.kind) {
    case 'commit_pushed': {
      const n = e.payload?.commits?.length ?? 0
      return t('issue.timeline.commitPushed', { name, n })
    }
    case 'branch_merged':
      return t('issue.timeline.branchMerged', {
        name,
        from: e.payload?.from_branch ?? '',
        into: e.payload?.into_branch ?? '',
        mode: e.payload?.mode ?? '',
      })
    case 'state_changed': {
      const to = e.payload?.to
      const from = e.payload?.from
      let transition = t('issue.timeline.transitionClosed')
      if (to === 'open' && from === 'closed') transition = t('issue.timeline.transitionOpenedFromClosed')
      else if (to === 'merged') transition = t('issue.timeline.transitionMerged')
      else if (to === 'open') transition = t('issue.timeline.transitionOpened')
      else if (to === 'closed') transition = t('issue.timeline.transitionClosed')
      return t('issue.timeline.stateChanged', { name, transition })
    }
    case 'title_changed':
      return t('issue.timeline.titleChanged', { name })
    case 'contribution_pushed':
      return t('issue.contributions.timeline.contributionPushed', {
        name,
        ref: e.payload?.ref_name ?? '',
        title: e.payload?.title ?? '',
      })
    case 'contribution_merged': {
      const contributor = e.payload?.agent_role ? `@agent-${e.payload.agent_role}` : '—'
      return t('issue.contributions.timeline.contributionMerged', {
        name,
        contributor,
        title: e.payload?.title ?? '',
        sha: shortSha(e.payload?.merge_commit_sha ?? ''),
      })
    }
    case 'contribution_rejected':
      return t('issue.contributions.timeline.contributionRejected', {
        name,
        ref: e.payload?.ref_name ?? '',
        reason: e.payload?.reason ?? '',
      })
    case 'contribution_closed':
      return t('issue.contributions.timeline.contributionClosed', {
        name,
        ref: e.payload?.ref_name ?? '',
      })
    case 'questionnaire_posted':
      return t('issue.timeline.questionnairePosted', {
        name,
        title: e.payload?.title ?? '',
      })
    default:
      return e.kind
  }
}



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
function verdictIcon(v: ReviewVerdict) {
  switch (v) {
    case 'approved': return ThumbsUp
    case 'rejected': return ThumbsDown
    case 'pending': return CircleSlash
  }
}


function todoStatusIcon(s: TodoStatus) {
  switch (s) {
    case 'todo': return Circle
    case 'in_progress': return CircleDot
    case 'done': return CircleCheck
  }
}
function todoStatusClass(s: TodoStatus) {
  switch (s) {
    case 'todo': return 'text-slate-500'
    case 'in_progress': return 'text-green-500'
    case 'done': return 'text-violet-500'
  }
}
function todoBadgeClass(s: TodoStatus) {
  switch (s) {
    case 'todo': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
    case 'in_progress': return 'bg-green-500/15 text-green-700 dark:text-green-300'
    case 'done': return 'bg-violet-500/15 text-violet-700 dark:text-violet-300'
  }
}


const cloneUrl = computed(() => {
  if (!repo.value) return ''
  const origin = import.meta.client ? window.location.origin : ''
  return `${origin}/git/${repo.value.owner_username}/${repo.value.name}.git`
})

const pushSnippet = computed(() => {
  if (!issue.value || !cloneUrl.value) return ''
  return [
    `git fetch origin`,
    `git checkout -b ${issue.value.branch_name} origin/${issue.value.base_branch}`,
    `# make changes`,
    `git add .`,
    `git commit -m "..."`,
    `git push -u origin ${issue.value.branch_name}`,
  ].join('\n')
})

// Auto-refresh keeps the page in sync with the server-side push pipeline:
// the PostReceive observer writes commit_pushed events after a CLI push, so
// every ~15s we re-pull issue + timeline + diff + children. We don't touch
// commentBody / parent / repo — those are either user-edited locally or
// immutable for the page's lifetime. Hidden tabs pause the poll so we don't
// burn requests while the user is elsewhere.
const REFRESH_INTERVAL_MS = 5_000
let refreshTimer: ReturnType<typeof setInterval> | null = null

async function refreshLive() {
  if (!issue.value) return
  if (issue.value.state === 'closed' || issue.value.state === 'merged') {
    stopRefreshTimer()
    return
  }
  await Promise.all([loadIssue(), loadTimeline(), loadDiff(), loadCommits(), loadChildren(), loadMentionAgents(), loadQuestionnaires()])
}

function startRefreshTimer() {
  if (refreshTimer || typeof window === 'undefined') return
  refreshTimer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    refreshLive()
  }, REFRESH_INTERVAL_MS)
}

function stopRefreshTimer() {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
}

onMounted(async () => {
  await loadRepo()
  await loadIssue()
  if (issue.value) {
    await Promise.all([
      loadTimeline(),
      loadDiff(),
      loadCommits(),
      loadParent(),
      loadChildren(),
      loadMentionAgents(),
      loadQuestionnaires(),
    ])
  }
  startRefreshTimer()
// Observe the sticky header so the sidebar's top offset stays in
// sync regardless of title wrap / multi-line layout shifts.
nextTick(() => {
  if (headerRef.value) {
    _headerObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        headerHeight.value = entry.contentRect.height
      }
    })
    _headerObserver.observe(headerRef.value)
  }

  // Observe the sidebar so sticky-bottom kicks in when cards
  // push the sidebar taller than the viewport.
  if (asideRef.value) {
    _asideObserver = new ResizeObserver((entries) => {
      for (const entry of entries) asideHeight.value = entry.contentRect.height
    })
    _asideObserver.observe(asideRef.value)
  }
})

})

onUnmounted(() => {
  stopRefreshTimer()
  _headerObserver?.disconnect()
  _asideObserver?.disconnect()
  _asideObserver = null
})
</script>

<template>
  <div class="space-y-6">
    <p v-if="issueError" class="text-sm text-destructive">{{ issueError }}</p>

<template v-if="issue">
<Tabs v-model="tab">
  <!-- Combined sticky header: title + meta + tabs bar -->
  <div ref="headerRef" class="sticky top-0 z-20 bg-background">
  <header class="space-y-2 pb-2 pt-0">
  <div class="flex flex-wrap items-center gap-2">
  <h1 class="text-2xl font-semibold tracking-tight">
  {{ issue.title }}
  <span class="text-muted-foreground">#{{ issue.number }}</span>
  </h1>
  <Badge :class="stateBadgeClass(issue.state)" variant="secondary">
  <component :is="stateBadgeIcon(issue.state)" class="mr-1 size-3" />
  {{ t(`issue.state.${issue.state}`) }}
  </Badge>
  </div>
  <p class="text-sm text-muted-foreground">
  {{ t('issue.openedBy', { name: issueActor(issue)?.display_name || issue.author_username || '—', time: rel(issue.created_at) }) }}
  </p>
  </header>
  <div class="border-b border-border overflow-x-auto pb-2">
  <TabsList>
  <TabsTrigger value="conversation">
  <MessageSquare class="size-4" />
  {{ t('issue.tabs.conversation') }}
  </TabsTrigger>
  <TabsTrigger value="commits">
  <GitCommit class="size-4" />
  {{ t('issue.tabs.commits') }}
  <span v-if="commits.length > 0" class="ml-1 text-xs text-muted-foreground">
  {{ commits.length }}
  </span>
  </TabsTrigger>
  <TabsTrigger value="diff">
  <DiffIcon class="size-4" />
  {{ t('issue.tabs.diff') }}
  </TabsTrigger>
	  <TabsTrigger value="contributions">
	    <FileDiffIcon class="size-4" />
	    {{ t('issue.tabs.contributions') }}
	  </TabsTrigger>

  <TabsTrigger value="agents">
  <Bot class="size-4" />
  {{ t('issue.tabs.agents') }}
  </TabsTrigger>

  <TabsTrigger v-if="children.length > 0" value="plan">
  <LayoutGrid class="size-4" />
  {{ t('issue.tabs.plan') }}
  </TabsTrigger>
  </TabsList>
  </div>
  </div>

  <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px] mt-4">
  <div class="min-w-0 space-y-4">

            <TabsContent value="conversation" class="space-y-3">
              <!-- Issue body: same comment-card shape as replies. The opener
                   is just the first comment with a different verb ("opened"
                   vs "commented") in the header strip. -->
              <Card class="gap-0 py-0">
                <CardContent class="p-0">
                  <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                    <ActorBadge :actor="issueActor(issue)" size="sm" />
                    <span class="text-muted-foreground">{{ t('issue.opened') }}</span>
                    <span class="text-muted-foreground" :title="formatDate(issue.created_at)">
                      {{ rel(issue.created_at) }}
                    </span>
                  </div>
                  <div class="px-4 py-3 text-sm">
                    <FoldableBody v-if="issue.body" :source="issue.body" />
                    <p v-else class="text-muted-foreground">—</p>
                  </div>
                </CardContent>
              </Card>

              <template v-for="it in timelineItems" :key="it.key">
                <!-- Comments render as full cards with a header strip — the
                     same layout GitHub uses on issue threads. Agent-authored
                     comments carry agent_role instead of an author_username
                     (the row has no user-table FK), so we render `@agent-<role>`
                     with a Bot avatar — mirroring the review_vote chrome. -->
                <Card v-if="it.kind === 'comment'" class="gap-0 py-0">
                  <CardContent class="p-0">
                    <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                      <ActorBadge :actor="commentActor(it.data)" size="sm" />
                      <span class="text-muted-foreground">{{ t('issue.commented') }}</span>
                      <span class="text-muted-foreground" :title="formatDate(it.data.created_at)">
                        {{ rel(it.data.created_at) }}
                      </span>
                    </div>
                    <div class="px-4 py-3 text-sm">
                      <FoldableBody :source="it.data.body" />
                    </div>
                  </CardContent>
                </Card>

                <!-- review_vote events get the same card chrome as comments
                     because the reason body is often substantive (the agent
                     explaining why) — burying it in a one-line event strip
                     would hide the audit trail humans need to trust the vote. -->
                <Card
                  v-else-if="it.kind === 'event' && it.data.kind === 'review_vote'"
                  class="gap-0 py-0"
                >
                  <CardContent class="p-0">
                    <div class="flex flex-wrap items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                      <ActorBadge :actor="eventActor(it.data)" size="sm" />
                      <Badge
                        :class="voteValueClass(it.data.payload?.value)"
                        variant="secondary"
                      >
                        <component :is="voteValueIcon(it.data.payload?.value)" class="mr-1 size-3" />
                        {{ t(`issue.review.vote.${it.data.payload?.value}`) }}
                      </Badge>
                      <span class="text-muted-foreground" :title="formatDate(it.data.created_at)">
                        · {{ rel(it.data.created_at) }}
                      </span>
                    </div>
                    <div class="px-4 py-3 text-sm">
                      <MarkdownBody v-if="it.data.payload?.reason" :source="it.data.payload.reason"  />
                      <p v-else class="text-muted-foreground">{{ t('issue.review.noReason') }}</p>
                    </div>
                  </CardContent>
                </Card>

                <!-- Questionnaires: agent-created surveys rendered as
                     first-class timeline cards with the same card chrome
                     as comments (ActorBadge header strip). The card
                     handles its own two states (unanswered placeholder,
                     answered with per-question results). -->
                <QuestionnaireTimelineCard
                  v-else-if="it.kind === 'questionnaire'"
                  :key="it.key"
                  :questionnaire="it.data"
                  :owner="owner"
                  :name="name"
                  :issue-number="Number(number)"
                  @submitted="refreshLive()"
                  @closed="refreshLive()"
                />

                <!-- System events render as a thin inline strip between
                     comments — they're context, not threads of their own,
                     so they shouldn't get the heavy card chrome. -->
                <div v-else class="flex items-start gap-2 px-1 text-xs text-muted-foreground">
                  <div class="mt-1 size-2 shrink-0 rounded-full bg-muted-foreground/40" />
                  <div class="min-w-0 flex-1 space-y-1">
                    <p>
                      {{ eventLabel(it.data) }}
                      <span :title="formatDate(it.at)">· {{ rel(it.at) }}</span>
                    </p>
                    <ul
                      v-if="it.data.kind === 'commit_pushed'"
                      class="space-y-0.5"
                    >
                      <li v-for="c in (it.data.payload?.commits ?? [])" :key="c.sha">
                        <NuxtLink
                          :to="`/${owner}/${name}/commits/${c.sha}`"
                          class="hover:text-foreground hover:underline"
                        >
                          <code class="font-mono">{{ shortSha(c.sha) }}</code>
                          — {{ c.message.split('\n', 1)[0] }}
                        </NuxtLink>
                      </li>
                    </ul>
                  </div>
                </div>
              </template>

              <!-- Compose card: matches GitHub's "Add a comment" footer.
                   Header strip carries the active user's identity, body has
                   a tall textarea, footer holds the submit button. Tight
                   inner padding so the textarea dominates the card. -->
              <Card v-if="issue.state === 'open'" class="gap-0 py-0">
                <CardContent class="p-0">
                  <div class="flex items-center gap-2 border-b bg-muted/40 px-3 py-2 text-xs">
                    <Avatar class="size-6 shrink-0">
                      <AvatarFallback class="bg-primary/10 text-[10px] text-primary">
                        {{ initialOf(user?.username) }}
                      </AvatarFallback>
                    </Avatar>
                    <span class="font-medium text-foreground">{{ user?.username ?? t('issue.you') }}</span>
                    <span class="text-muted-foreground">{{ t('issue.commentForm.title') }}</span>
                  </div>
                  <div class="space-y-2 px-3 py-3">
                    <p v-if="hostYamlError" class="text-sm text-destructive">
                      {{ t('issue.commentForm.hostYamlError') }}: {{ hostYamlError }}
                    </p>
                    <AttachmentUploader
                      @insert="(snippet: string) => mentionTextareaRef?.insertAtCursor(snippet)"
                    />
                    <MentionTextarea
                      ref="mentionTextareaRef"
                      v-model="commentBody"
                      :suggestions="mentionAgents"
                      rows="8"
                      class="min-h-44 resize-y text-sm leading-relaxed"
                      :placeholder="t('issue.commentForm.placeholder')"
                    />
                    <p v-if="commentError" class="text-sm text-destructive">
                      {{ commentError }}
                    </p>
                    <div class="flex justify-end">
                      <Button :disabled="commentBusy || !commentBody.trim()" @click="submitComment">
                        {{ commentBusy ? t('issue.commentForm.submitting') : t('issue.commentForm.submit') }}
                      </Button>
                    </div>
                  </div>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="commits" class="space-y-4">
              <Card v-if="commits.length === 0" class="gap-0 py-0">
                <CardContent class="p-4 text-sm text-muted-foreground">
                  {{ t('issue.commitsEmpty', { branch: issue.branch_name }) }}
                </CardContent>
              </Card>
              <Card v-else class="gap-0 py-0">
                <CardContent class="p-0">
                  <ul class="divide-y">
                    <li v-for="c in commits" :key="c.sha" class="hover:bg-muted/30">
                      <NuxtLink
                        :to="`/${owner}/${name}/commits/${c.sha}`"
                        class="flex items-center gap-3 px-4 py-2.5"
                      >
                        <GitCommit class="size-4 shrink-0 text-muted-foreground" />
                        <div class="min-w-0 flex-1">
                          <p class="truncate text-sm font-medium">
                            {{ c.message.split('\n', 1)[0] }}
                          </p>
                          <p class="text-xs text-muted-foreground">
                            {{ c.author.name }}
                            <span :title="formatDate(c.committed_at)">· {{ rel(c.committed_at) }}</span>
                          </p>
                        </div>
                        <code class="hidden font-mono text-xs text-muted-foreground sm:inline">
                          {{ shortSha(c.sha) }}
                        </code>
                      </NuxtLink>
                    </li>
                  </ul>
                </CardContent>
              </Card>
            </TabsContent>

            <TabsContent value="diff" class="space-y-4">
              <p v-if="diffError" class="text-sm text-destructive">{{ diffError }}</p>
              <Card v-if="diff.length === 0" class="gap-0">
                <CardContent class="p-4 text-sm text-muted-foreground">
                  {{ t('issue.diffEmpty', { branch: issue.branch_name }) }}
                </CardContent>
              </Card>
              <FileDiffList
                v-else
                :diffs="diff"
                :owner="owner"
                :name="name"
                :ref-before="issue.base_branch"
                :ref-after="issue.branch_name"
              />
            </TabsContent>

            <TabsContent value="contributions" class="space-y-4">
              <ContributionsView
                :active="tab === 'contributions'"
                :owner="owner"
                :name="name"
                :issue-number="Number(number)"
                :can-manage="canMerge"
                @applied="() => { loadIssue(); loadTimeline(); loadDiff(); loadCommits(); }"
                @closed="() => loadTimeline()"
              />
            </TabsContent>


            <TabsContent value="agents" class="space-y-4">
              <AgentSessionsView
                :active="tab === 'agents'"
                :owner="owner"
                :name="name"
                :issue-number="Number(number)"
              />
    </TabsContent>

            <TabsContent value="plan" class="space-y-4">
              <PlanView
                :owner="owner"
                :name="name"
                :issue-number="Number(number)"
              />
            </TabsContent>
  </div>

  <aside
  ref="asideRef"
  class="sticky self-start space-y-4 pb-6"
  :style="{ top: `${stickyTop}px` }"
  >
          <Card class="gap-0 py-0">
            <CardContent class="space-y-3 p-4 text-sm">
              <div class="flex items-center gap-2 text-xs text-muted-foreground">
                <GitBranch class="size-3" />
                <span>{{ t('issue.branch') }}</span>
              </div>
              <code class="block break-all rounded bg-muted/40 px-2 py-1 font-mono text-xs">
                {{ issue.branch_name }}
              </code>
              <div class="text-xs text-muted-foreground">
                {{ t('issue.base') }}:
                <code class="font-mono">{{ issue.base_branch }}</code>
              </div>
              <div class="text-xs text-muted-foreground">
                {{ t('issue.headSHA') }}:
                <code v-if="issue.head_sha" class="font-mono">{{ shortSha(issue.head_sha) }}</code>
                <span v-else>{{ t('issue.noHead') }}</span>
              </div>
              <div v-if="issue.merge_commit_sha" class="text-xs text-muted-foreground">
                {{ t('issue.mergedSHA') }}:
                <code class="font-mono">{{ shortSha(issue.merge_commit_sha) }}</code>
              </div>
              <!-- Branch totals: only meaningful once there's anything on
                   the issue branch beyond base. Hidden for unborn / empty
                   branches so the sidebar doesn't show "0 files / +0 −0". -->
              <div
                v-if="diffStats.filesChanged > 0"
                class="space-y-1 border-t pt-3 text-xs text-muted-foreground"
              >
                <p class="flex items-center gap-2 text-xs text-muted-foreground">
                  <DiffIcon class="size-3" />
                  {{ t('issue.diffStats') }}
                </p>
                <p>{{ t('issue.filesChanged', { n: diffStats.filesChanged }) }}</p>
                <p class="font-mono">
                  <span class="text-emerald-600 dark:text-emerald-400">+{{ diffStats.added }}</span>
                  <span class="ml-2 text-red-600 dark:text-red-400">−{{ diffStats.deleted }}</span>
                </p>
              </div>
            </CardContent>
          </Card>

          <Card v-if="parent" class="gap-0 py-0">
            <CardContent class="space-y-2 p-4 text-sm">
              <p class="flex items-center gap-1 text-xs text-muted-foreground">
                <CornerDownRight class="size-3" />
                {{ t('issue.parent') }}
              </p>
              <NuxtLink
                :to="`/${owner}/${name}/issues/${parent.number}`"
                class="block truncate text-sm font-medium hover:underline"
              >
                #{{ parent.number }} {{ parent.title }}
              </NuxtLink>
              <Badge :class="stateBadgeClass(parent.state)" variant="secondary">
                {{ t(`issue.state.${parent.state}`) }}
              </Badge>
            </CardContent>
          </Card>

  <!-- Reviews: server-computed review_status is the single source of
  truth. The verdict shows the current gate outcome (approved /
  rejected / pending) against the issue's current
  head_sha. Valid votes are those cast on the current head_sha;
  stale votes are recorded in the timeline but no longer count. -->
  <Card class="gap-0 py-0">
  <CardContent class="space-y-3 p-4">
  <div class="flex items-center justify-between gap-2">
  <p class="flex items-center gap-1 text-xs text-muted-foreground">
  <ThumbsUp class="size-3" />
  {{ t('issue.review.title') }}
  </p>
  <Badge
  v-if="reviewStatus"
  :class="verdictClass(reviewStatus.verdict)"
  variant="secondary"
  >
  <component :is="verdictIcon(reviewStatus.verdict)" class="mr-1 size-3" />
  {{ t(`issue.review.verdict.${reviewStatus.verdict}`) }}
  </Badge>
  <Badge v-else :class="verdictClass('pending')" variant="secondary">
  <component :is="verdictIcon('pending')" class="mr-1 size-3" />
  {{ t('issue.review.verdict.pending') }}
  </Badge>
  </div>

  <!-- Based on which head_sha -->
  <p
  v-if="reviewStatus?.head_sha"
  class="text-xs text-muted-foreground"
  >
  {{ t('issue.review.basedOn', { sha: shortSha(reviewStatus.head_sha) }) }}
  </p>

  <!-- Valid vote list -->
  <p
  v-if="!reviewStatus || reviewStatus.votes.length === 0"
  class="text-xs text-muted-foreground"
  >
  {{ t('issue.review.empty') }}
  </p>
  <ul v-else class="space-y-1.5">
  <li
  v-for="v in reviewStatus.votes"
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

  <!-- Stale votes warning -->
  <p
  v-if="reviewStatus?.stale_votes?.length"
  class="text-xs text-amber-600 dark:text-amber-400"
  >
  {{ t('issue.review.staleWarning', { n: reviewStatus.stale_votes.length }) }}
  </p>
  </CardContent>
  </Card>


          <!-- Todos: server-embedded todo list with summary counts and
               detail items. Display-only — no inline editing. -->
          <Card class="gap-0 py-0">
            <CardContent class="space-y-3 p-4">
              <div class="flex items-center justify-between gap-2">
                <p class="flex items-center gap-1 text-xs text-muted-foreground">
                  <ListTodo class="size-3" />
                  {{ t('issue.todos.title') }}
                </p>
                <Badge
                  v-if="todos.length > 0"
                  :class="todoSummary?.all_done ? 'bg-violet-500/15 text-violet-700 dark:text-violet-300' : 'bg-slate-500/15 text-slate-700 dark:text-slate-300'"
                  variant="secondary"
                >
                  {{ todoSummary?.done ?? 0 }} / {{ todoSummary?.total ?? 0 }} {{ t('issue.todos.completed') }}
                </Badge>
              </div>

              <!-- Empty state -->
              <p v-if="todos.length === 0" class="text-xs text-muted-foreground">
                {{ t('issue.todos.empty') }}
              </p>

              <template v-else>
                <!-- Three-state summary counts -->
                <div v-if="todoSummary" class="flex gap-3 text-xs text-muted-foreground">
                  <Badge variant="secondary" class="bg-slate-500/15 text-slate-700 dark:text-slate-300">
                    {{ t('issue.todos.status.todo') }} {{ todoSummary.todo }}
                  </Badge>
                  <Badge variant="secondary" class="bg-green-500/15 text-green-700 dark:text-green-300">
                    {{ t('issue.todos.status.in_progress') }} {{ todoSummary.in_progress }}
                  </Badge>
                  <Badge variant="secondary" class="bg-violet-500/15 text-violet-700 dark:text-violet-300">
                    {{ t('issue.todos.status.done') }} {{ todoSummary.done }}
                  </Badge>
                </div>

                <!-- Detail list -->
                <ul class="space-y-1.5">
                  <li v-for="item in todos" :key="item.id" class="flex items-start gap-2 text-xs">
                    <component :is="todoStatusIcon(item.status)" class="size-3 shrink-0 mt-0.5" :class="todoStatusClass(item.status)" />
                    <span class="min-w-0 flex-1 whitespace-normal break-words">{{ item.content }}</span>
                    <Badge :class="todoBadgeClass(item.status)" variant="secondary" class="shrink-0">
                      {{ t(`issue.todos.status.${item.status}`) }}
                    </Badge>
                  </li>
                </ul>
              </template>
            </CardContent>
          </Card>

          <!-- CI Checks: issue-level workflow check status -->
          <Card class="gap-0 py-0">
            <CardContent class="space-y-3 p-4">
              <p class="flex items-center gap-1 text-xs text-muted-foreground">
                <Play class="size-3" />
                {{ t('issue.checks.title') }}
              </p>
              <CheckRunPanel
                :owner="owner"
                :name="name"
                :issue-number="number"
              />
            </CardContent>
          </Card>

          <Card class="gap-0 py-0">
            <CardContent class="space-y-3 p-4">
              <div class="flex items-center justify-between gap-2">
                <p class="text-xs text-muted-foreground">{{ t('issue.subIssues') }}</p>
                <Button
                  v-if="issue.state === 'open'"
                  size="sm"
                  variant="ghost"
                  class="h-7 gap-1 px-2 text-xs"
                  as-child
                >
                  <NuxtLink :to="`/${owner}/${name}/issues/new?parent=${issue.number}`">
                    <Plus class="size-3" />
                    {{ t('issue.newSubIssue') }}
                  </NuxtLink>
                </Button>
              </div>
              <p v-if="children.length === 0" class="text-xs text-muted-foreground">
                {{ t('issue.noSubIssues') }}
              </p>
              <ul v-else class="space-y-1.5">
                <li v-for="kid in children" :key="kid.id" class="flex items-center gap-2 text-sm">
                  <component :is="stateBadgeIcon(kid.state)" class="size-3 shrink-0 text-muted-foreground" />
                  <NuxtLink
                    :to="`/${owner}/${name}/issues/${kid.number}`"
                    class="min-w-0 flex-1 truncate hover:underline"
                  >
                    <span class="text-muted-foreground">#{{ kid.number }}</span>
                    {{ kid.title }}
                  </NuxtLink>
                </li>
              </ul>
            </CardContent>
          </Card>

          <Card v-if="!issue.head_sha && issue.state === 'open'" class="gap-0 py-0">
            <CardContent class="space-y-2 p-4">
              <p class="text-xs text-muted-foreground">{{ t('issue.pushHint') }}</p>
              <pre class="overflow-x-auto rounded bg-muted/40 p-2 font-mono text-xs leading-relaxed"><code>{{ pushSnippet }}</code></pre>
            </CardContent>
          </Card>

  <div v-if="actionError" class="space-y-1">
    <p class="text-sm text-destructive">{{ actionError }}</p>
    <ul v-if="actionSubIssues.length > 0" class="space-y-0.5">
      <li v-for="si in actionSubIssues" :key="si.id" class="text-sm text-destructive">
        <NuxtLink :to="`/${owner}/${name}/issues/${si.number}`" class="font-mono hover:underline">
          #{{ si.number }}
        </NuxtLink>
        {{ si.title }}
        <span v-if="si.depth > 1" class="text-muted-foreground text-xs">
          ({{ t('issue.subIssueBlockDeep', { n: si.depth - 1 }) }})
        </span>
      </li>
    </ul>
  </div>
  <div v-if="actionInfo" class="text-sm text-emerald-700 dark:text-emerald-400">{{ actionInfo }}</div>

  <div class="space-y-2">
  <Button
  v-if="canMerge"
  class="w-full"
  :disabled="mergeBusy || mergeBlocked || subIssueBlocked"
  @click="merge"
  >
  <GitMerge class="size-4" />
  {{ mergeBusy ? t('issue.merging') : t('issue.merge') }}
  </Button>
  <p v-if="canMerge && mergeBlocked" class="text-xs text-destructive">
  {{ mergeBlockReason }}
  </p>
  <div v-if="canMerge && subIssueBlocked" class="space-y-0.5">
    <p class="text-xs text-destructive">{{ subIssueBlockMessage }}</p>
    <ul class="space-y-0.5 text-xs text-destructive">
      <li v-for="kid in openSubIssues" :key="kid.id">
        <NuxtLink :to="`/${owner}/${name}/issues/${kid.number}`" class="font-mono hover:underline">
          #{{ kid.number }}
        </NuxtLink>
        {{ kid.title }}
      </li>
    </ul>
  </div>

  <Button
  v-if="issue.state !== 'merged' && (issue.author_id === user?.id || issue.actor?.user_id === user?.id || canManage)"
              variant="outline"
              class="w-full"
              :disabled="stateBusy || (issue.state === 'open' && subIssueBlocked)"
              @click="toggleState"
            >
              {{ issue.state === 'open' ? t('issue.close') : t('issue.reopen') }}
            </Button>
  <div v-if="issue.state === 'open' && subIssueBlocked" class="space-y-0.5">
    <p class="text-xs text-destructive">{{ subIssueBlockMessage }}</p>
    <ul class="space-y-0.5 text-xs text-destructive">
      <li v-for="kid in openSubIssues" :key="kid.id">
        <NuxtLink :to="`/${owner}/${name}/issues/${kid.number}`" class="font-mono hover:underline">
          #{{ kid.number }}
        </NuxtLink>
        {{ kid.title }}
      </li>
    </ul>
  </div>
          </div>
    </aside>
    </div>
  </Tabs>
  </template>

  <!-- Scroll-to-top floating button -->
  <Transition name="scroll-top">
    <button
      v-if="showScrollTop"
      :aria-label="t('issue.scrollToTop')"
      class="fixed bottom-6 right-6 z-30 flex size-11 items-center justify-center rounded-full bg-background shadow-md border border-border text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      @click="scrollToTop"
    >
      <ArrowUp class="size-5" />
    </button>
  </Transition>
  </div>
</template>

<style scoped>
.scroll-top-enter-active,
.scroll-top-leave-active {
  transition: opacity 0.2s ease-out, transform 0.2s ease-out;
}
.scroll-top-enter-from,
.scroll-top-leave-to {
  opacity: 0;
  transform: translateY(8px);
  pointer-events: none;
}
</style>
