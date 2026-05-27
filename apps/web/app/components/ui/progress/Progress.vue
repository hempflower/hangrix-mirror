<script setup lang="ts">
import type { HTMLAttributes } from "vue"
import { cn } from '@/utils/utils'

interface ProgressProps {
  class?: HTMLAttributes["class"]
  modelValue?: number
  max?: number
}

const props = withDefaults(defineProps<ProgressProps>(), {
  max: 100,
  modelValue: 0,
})
</script>

<template>
  <div
    data-slot="progress"
    role="progressbar"
    :aria-valuenow="modelValue"
    :aria-valuemax="max"
    :class="cn('relative h-2 w-full overflow-hidden rounded-full bg-primary/10', props.class)"
  >
    <div
      class="h-full rounded-full bg-primary transition-all duration-200"
      :style="{ width: `${Math.min(100, Math.max(0, (modelValue / max) * 100))}%` }"
    />
  </div>
</template>
