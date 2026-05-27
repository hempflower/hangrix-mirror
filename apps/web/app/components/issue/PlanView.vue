<script setup lang="ts">
import { Skeleton } from '@/components/ui/skeleton'
import { Card, CardContent } from '@/components/ui/card'
import ActorBadge from '@/components/ActorBadge.vue'
import PlanHeader from './plan/PlanHeader.vue'
import PlanNode from './plan/PlanNode.vue'
import { usePlan } from '@/composables/usePlan'
import type { PlanNode as PlanNodeType } from '~/types/issue'

const props = defineProps<{
  owner: string
  name: string
  issueNumber: number
}>()

const { t } = useI18n()

const {
  data,
  isLoading,
  error,
  collapsed,
  hideDone,
  refresh,
} = usePlan(() => props.owner, () => props.name, () => props.issueNumber)

function toggleCollapse(number: number) {
  if (collapsed.has(number)) {
    collapsed.delete(number)
  } else {
    collapsed.add(number)
  }
}

// Count total nodes
function countNodes(n: PlanNodeType): number {
  let count = 1
  for (const c of n.children) count += countNodes(c)
  return count
}

const totalNodes = computed(() => {
  if (!data.value?.root) return 0
  return countNodes(data.value.root)
})

const readyNumbers = computed(() => data.value?.ready ?? [])

// --- Mobile helpers ---

// subtreeAllMerged: true when node and all descendants are merged
function subtreeAllMerged(n: PlanNodeType): boolean {
  if (n.state !== 'merged') return false
  return n.children.every(subtreeAllMerged)
}

function hiddenByHideDone(n: PlanNodeType): boolean {
  if (!hideDone.value) return false
  return subtreeAllMerged(n)
}

// Flatten the tree into a DFS-ordered list with depth info.
// Respects collapsed state: collapsed subtrees are skipped.
// Respects hideDone filter.
interface FlatNode extends PlanNodeType {
  depth: number
}

function flattenTree(root: PlanNodeType): FlatNode[] {
  const result: FlatNode[] = []
  function walk(n: PlanNodeType, depth: number) {
    if (hiddenByHideDone(n)) return
    result.push({ ...n, depth })
    if (!collapsed.has(n.number)) {
      for (const c of n.children) walk(c, depth + 1)
    }
  }
  walk(root, 0)
  return result
}
</script>

<template>
  <div class="space-y-4">
    <!-- Loading state: skeleton -->
    <template v-if="isLoading">
      <div class="space-y-3">
        <!-- Header skeleton -->
        <div class="flex items-center gap-3">
          <Skeleton class="h-4 w-32" />
          <Skeleton class="h-4 w-24" />
        </div>
        <div class="flex items-center gap-2">
          <Skeleton class="h-4 w-20" />
          <Skeleton class="h-4 w-16" />
        </div>
        <!-- Tree skeleton -->
        <div class="space-y-2 pt-2">
          <Skeleton v-for="i in 6" :key="i" class="h-6" :style="{ width: `${70 + (i % 3) * 10}%` }" />
        </div>
      </div>
    </template>

    <!-- Error + empty state (no data ever loaded) -->
    <template v-else-if="!data">
      <Card class="gap-0 py-0">
        <CardContent class="p-4 text-center text-sm text-muted-foreground">
          <p class="font-medium">{{ t('issue.plan.empty.title') }}</p>
          <p class="mt-1 text-xs">{{ t('issue.plan.empty.hint') }}</p>
        </CardContent>
      </Card>
    </template>

    <!-- Normal state -->
    <template v-else>
      <PlanHeader
        :root="data.root"
        :rollup="data.rollup"
        :ready="readyNumbers"
        :heuristic="data.heuristic ?? false"
        :hide-done="hideDone"
        :error="error"
        :total-nodes="totalNodes"
        @update:hide-done="hideDone = $event"
        @retry="refresh()"
      />

      <!-- Tree (desktop ≥640px) -->
      <div class="hidden sm:block">
        <PlanNode
          :node="data.root"
          :depth="0"
          :collapsed="collapsed"
          :hide-done="hideDone"
          @toggle="toggleCollapse"
        />
      </div>

      <!-- Card list (mobile <640px) -->
      <div class="space-y-2 sm:hidden">
        <Card
          v-for="node in flattenTree(data.root)"
          :key="node.number"
          class="gap-0 py-0"
          :class="node.state === 'closed' ? 'opacity-60' : ''"
        >
          <NuxtLink :to="`./${node.number}`" class="block">
            <CardContent class="flex items-center gap-3 p-3">
              <!-- Depth indicator: left color bar -->
              <div
                class="h-10 w-1 shrink-0 rounded-full"
                :style="{ opacity: 1 - (node as FlatNode).depth * 0.15 }"
                :class="{
                  'bg-violet-400': node.state === 'merged',
                  'bg-slate-400': node.state === 'closed',
                  'bg-emerald-400': node.state === 'open',
                }"
              />

              <div class="min-w-0 flex-1">
                <!-- Row 1: # + title -->
                <div class="flex items-center gap-1.5">
                  <span class="text-xs text-muted-foreground shrink-0">#{{ node.number }}</span>
                  <span class="truncate text-sm font-medium" :class="node.state === 'closed' ? 'line-through' : ''">
                    {{ node.title }}
                  </span>
                </div>

                <!-- Row 2: status + role + chips -->
                <div class="mt-0.5 flex flex-wrap items-center gap-1.5 text-xs">
                  <span
                    class="rounded px-1.5 py-0 text-xs"
                    :class="{
                      'bg-violet-500/15 text-violet-700 dark:text-violet-300': node.state === 'merged',
                      'bg-slate-500/15 text-slate-700 dark:text-slate-300': node.state === 'closed',
                      'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300': node.state === 'open',
                    }"
                  >
                    {{ node.state }}
                  </span>

                  <ActorBadge
                    v-if="node.actor || node.agent_role"
                    :actor="node.actor ?? { kind: 'agent', id: `agent:${node.agent_role}`, display_name: `@agent-${node.agent_role}`, role_key: node.agent_role }"
                    size="sm"
                  />

                  <!-- Todo mini -->
                  <span
                    v-if="node.todo_summary && node.todo_summary.total > 0"
                    class="text-muted-foreground"
                  >
                    {{ node.todo_summary.done }}/{{ node.todo_summary.total }}
                  </span>
                </div>
              </div>

              <!-- Collapse indicator for non-leaf -->
              <span
                v-if="node.children && node.children.length > 0"
                class="text-xs text-muted-foreground shrink-0"
              >
                {{ collapsed.has(node.number) ? `+${node.children.length}` : `−` }}
              </span>
            </CardContent>
          </NuxtLink>
        </Card>
      </div>
    </template>
  </div>
</template>
