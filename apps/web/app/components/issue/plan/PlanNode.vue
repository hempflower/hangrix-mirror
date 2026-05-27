<script setup lang="ts">
import {
  Ban,
  ChevronDown,
  ChevronRight,
  Circle,
  CircleDot,
  GitMerge,
  Lock,
} from 'lucide-vue-next'
import ActorBadge from '@/components/ActorBadge.vue'
import PlanReadyChip from './PlanReadyChip.vue'
import type { PlanNode } from '~/types/issue'

const props = defineProps<{
  node: PlanNode
  depth: number
  collapsed: Set<number>
  hideDone: boolean
}>()

const emit = defineEmits<{
  (e: 'toggle', number: number): void
}>()

const hasChildren = computed(() => props.node.children.length > 0)
const isCollapsed = computed(() => props.collapsed.has(props.node.number))

function toggle(e: Event) {
  e.stopPropagation()
  e.preventDefault()
  emit('toggle', props.node.number)
}

const stateIcon = computed(() => {
  if (props.node.state === 'merged') return GitMerge
  if (props.node.state === 'closed') return Lock
  return CircleDot
})

const stateIconClass = computed(() => {
  if (props.node.state === 'merged') return 'text-violet-500'
  if (props.node.state === 'closed') return 'text-slate-400'
  if (props.node.state === 'open') return 'text-emerald-500'
  return 'text-slate-500'
})

const isLeaf = computed(() => !hasChildren.value)

// Review status indicator
const reviewBadge = computed(() => {
  const rs = props.node.review_status
  if (!rs) return null
  if (rs.verdict === 'pending' && rs.votes.length > 0 && rs.required_reviewers.length > 0) {
    return { text: `${rs.votes.filter(v => v.value === 'approve').length}/${rs.required_reviewers.length}✓`, class: 'bg-amber-500/15 text-amber-700 dark:text-amber-300' }
  }
  return null
})

// Todo mini-progress
const todoMini = computed(() => {
  const ts = props.node.todo_summary
  if (!ts || ts.total === 0) return null
  return `${ts.done}/${ts.total}`
})

// --- subtreeAllMerged: true when this node and all its descendants are merged ---
function subtreeAllMerged(n: PlanNode): boolean {
  if (n.state !== 'merged') return false
  return n.children.every(subtreeAllMerged)
}

// Whether this node should be hidden when hideDone is on
// UX §3 exception: a merged parent with any non-merged child stays visible
const hiddenByHideDone = computed(() => {
  if (!props.hideDone) return false
  return subtreeAllMerged(props.node)
})
</script>

<template>
  <template v-if="!hiddenByHideDone">
    <!-- Tree node row -->
    <NuxtLink
      :to="`./${node.number}`"
      class="group flex items-center gap-1.5 rounded px-2 py-1.5 text-sm hover:bg-muted/50"
      :class="[
        node.state === 'closed' ? 'opacity-60 line-through' : '',
      ]"
      :style="{ paddingLeft: `${depth * 20 + 8}px` }"
      role="link"
      :tabindex="0"
      :aria-label="`#${node.number} ${node.title}`"
    >
      <!-- Collapse toggle (only for non-leaf) -->
      <button
        v-if="hasChildren"
        class="flex size-4 shrink-0 items-center justify-center rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        :tabindex="0"
        :aria-label="isCollapsed ? 'Expand' : 'Collapse'"
        @click="toggle"
      >
        <ChevronRight v-if="isCollapsed" class="size-3.5" />
        <ChevronDown v-else class="size-3.5" />
      </button>
      <span v-else class="inline-block w-4 shrink-0" />

      <!-- State icon -->
      <component :is="stateIcon" :class="stateIconClass" class="size-3.5 shrink-0" />

      <!-- Issue number + title -->
      <span class="text-muted-foreground shrink-0">#{{ node.number }}</span>
      <span class="truncate font-medium">{{ node.title }}</span>

      <!-- Actor badge -->
      <ActorBadge
        v-if="node.actor || node.agent_role"
        :actor="node.actor ?? { kind: 'agent', id: `agent:${node.agent_role}`, display_name: `@agent-${node.agent_role}`, role_key: node.agent_role }"
        size="sm"
        class="shrink-0"
      />

      <!-- State badge -->
      <span
        class="shrink-0 rounded px-1.5 py-0.5 text-xs font-medium"
        :class="{
          'bg-violet-500/15 text-violet-700 dark:text-violet-300': node.state === 'merged',
          'bg-slate-500/15 text-slate-700 dark:text-slate-300': node.state === 'closed',
          'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300': node.state === 'open',
        }"
      >
        <component :is="stateIcon" class="mr-0.5 inline size-2.5" />
        {{ node.state }}
      </span>

      <!-- Review badge -->
      <span
        v-if="reviewBadge"
        :class="reviewBadge.class"
        class="shrink-0 rounded px-1.5 py-0.5 text-xs font-medium"
      >
        {{ reviewBadge.text }}
      </span>

      <!-- Todo mini progress -->
      <span
        v-if="todoMini"
        class="shrink-0 text-xs text-muted-foreground"
      >
        {{ todoMini }}
      </span>

      <!-- Ready chip -->
      <PlanReadyChip
        v-if="node.ready"
        :number="node.number"
        :title="node.title"
      />

      <!-- Blocked chip -->
      <span
        v-if="node.blocked && node.depends_on.length > 0"
        class="inline-flex shrink-0 items-center gap-0.5 rounded bg-red-500/10 px-1.5 py-0.5 text-xs font-medium text-red-700 dark:text-red-300"
      >
        <Ban class="size-3" />
        <template v-for="(dep, i) in node.depends_on" :key="dep">
          <span v-if="i > 0">,</span>
          #{{ dep }}
        </template>
      </span>

      <!-- Leaf children count (for non-leaf nodes) -->
      <span
        v-if="hasChildren"
        class="shrink-0 text-xs text-muted-foreground"
      >
        ({{ node.children.length }})
      </span>
    </NuxtLink>

    <!-- Children (recursive, v-if for performance) -->
    <template v-if="hasChildren && !isCollapsed">
      <PlanNode
        v-for="child in node.children"
        :key="child.number"
        :node="child"
        :depth="depth + 1"
        :collapsed="collapsed"
        :hideDone="hideDone"
        @toggle="emit('toggle', $event)"
      />
    </template>
  </template>
</template>
