<script setup lang="ts">
import {
  Info,
  RefreshCw,
} from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import ProgressBar from '@/components/ui/progress/Progress.vue'
import { Checkbox } from '@/components/ui/checkbox'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import PlanReadyChip from './PlanReadyChip.vue'
import type { PlanNode, PlanRollup } from '~/types/issue'

const { t } = useI18n()

const props = defineProps<{
  root: PlanNode | null
  rollup: PlanRollup | null
  ready: number[]
  heuristic: boolean
  hideDone: boolean
  error: string | null
  totalNodes: number
}>()

const emit = defineEmits<{
  (e: 'update:hideDone', value: boolean): void
  (e: 'retry'): void
}>()

const progressPct = computed(() => {
  if (!props.rollup || props.rollup.total_leaves === 0) return 0
  return (props.rollup.merged / props.rollup.total_leaves) * 100
})

// Build a map of ready number → title for chips
const readyNodes = computed(() => {
  if (!props.root) return []
  const map = new Map<number, string>()
  function walk(n: PlanNode) {
    map.set(n.number, n.title)
    n.children.forEach(walk)
  }
  walk(props.root)
  return props.ready.map(num => ({ number: num, title: map.get(num) ?? `#${num}` }))
})
</script>

<template>
  <div class="space-y-3">
    <!-- Error banner -->
    <div
      v-if="error"
      class="flex items-center gap-2 rounded bg-red-500/10 px-3 py-2 text-sm text-red-700 dark:text-red-300"
    >
      <span class="flex-1">{{ t('issue.plan.error.banner') }}</span>
      <Button variant="outline" size="sm" class="h-7 gap-1 text-xs" @click="emit('retry')">
        <RefreshCw class="size-3" />
        {{ t('issue.plan.error.retry') }}
      </Button>
    </div>

    <!-- Progress & ready bar -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1">
      <div class="flex items-center gap-2 min-w-0">
        <ProgressBar
          :model-value="progressPct"
          class="h-2 w-32 transition-all duration-200"
        />
        <span class="text-sm text-muted-foreground whitespace-nowrap">
          {{ t('issue.plan.progress', { merged: rollup?.merged ?? 0, total: rollup?.total_leaves ?? 0 }) }}
        </span>
      </div>

      <!-- Ready chips -->
      <template v-if="ready.length > 0">
        <span class="text-sm text-muted-foreground whitespace-nowrap">
          {{ t('issue.plan.ready') }}
        </span>
        <span class="inline-flex flex-wrap gap-1">
          <PlanReadyChip
            v-for="r in readyNodes"
            :key="r.number"
            :number="r.number"
            :title="r.title"
          />
        </span>
      </template>

      <!-- Heuristic indicator -->
      <Tooltip v-if="heuristic">
        <TooltipTrigger as="span">
          <span class="inline-flex cursor-help items-center gap-0.5 text-xs text-amber-600 dark:text-amber-400">
            <Info class="size-3" />
            {{ t('issue.plan.heuristic') }}
          </span>
        </TooltipTrigger>
        <TooltipContent side="top">
          {{ t('issue.plan.heuristicTip') }}
        </TooltipContent>
      </Tooltip>
    </div>

    <!-- Toolbar: hide-completed + engine placeholder -->
    <div class="flex flex-wrap items-center justify-between gap-2">
      <label class="flex cursor-pointer items-center gap-1.5 text-sm text-muted-foreground">
        <Checkbox
          :model-value="hideDone"
          @update:model-value="(v) => emit('update:hideDone', v === true)"
        />
        {{ t('issue.plan.hideDone') }}
      </label>

      <!-- Engine placeholder — phase 0, not functional -->
      <span class="text-xs text-muted-foreground/50">
        ⏸ {{ t('issue.plan.engine.paused') }}
      </span>
    </div>

    <!-- Large tree hint -->
    <p
      v-if="totalNodes > 200"
      class="text-xs text-amber-600 dark:text-amber-400"
    >
      {{ t('issue.plan.treeLarge') }}
    </p>
  </div>
</template>
