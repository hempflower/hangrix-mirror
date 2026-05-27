<script setup lang="ts">
import { Play } from 'lucide-vue-next'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

const props = defineProps<{
  number: number
  title: string
}>()

const shortTitle = computed(() => {
  if (props.title.length > 40) {
    return props.title.slice(0, 40) + '…'
  }
  return props.title
})
</script>

<template>
  <Tooltip>
    <TooltipTrigger as="span">
      <NuxtLink
        :to="`./${number}`"
        class="inline-flex items-center gap-0.5 rounded bg-emerald-500/10 px-1.5 py-0.5 text-xs font-medium text-emerald-700 hover:bg-emerald-500/20 hover:underline dark:text-emerald-300"
      >
        <Play class="size-3" />
        #{{ number }}
      </NuxtLink>
    </TooltipTrigger>
    <TooltipContent side="top">
      {{ shortTitle }}
    </TooltipContent>
  </Tooltip>
</template>
