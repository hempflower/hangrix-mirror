<script setup lang="ts">
import { Bot, Cog, GitBranch } from 'lucide-vue-next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import type { ActorRef } from '~/types/actor'

const props = withDefaults(
  defineProps<{
    actor: ActorRef | null | undefined
    /** Size variant. */
    size?: 'sm' | 'default'
  }>(),
  { size: 'default' },
)

function initialOf(s: string): string {
  return s ? s.charAt(0).toUpperCase() : '?'
}
</script>

<template>
  <template v-if="actor">
    <!-- user → avatar + username -->
    <template v-if="actor.kind === 'user'">
      <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
        <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
          {{ initialOf(actor.display_name) }}
        </AvatarFallback>
      </Avatar>
      <span class="font-medium text-foreground">{{ actor.display_name || '—' }}</span>
    </template>

    <!-- agent → bot icon + @agent-role -->
    <template v-else-if="actor.kind === 'agent'">
      <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
        <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
          <Bot :class="size === 'sm' ? 'size-2.5' : 'size-3'" />
        </AvatarFallback>
      </Avatar>
      <span
        class="font-medium text-foreground"
        :title="`@agent-${actor.role_key || actor.display_name}`"
      >
        {{ actor.display_name }}
      </span>
    </template>

    <!-- workflow → branch icon + name -->
    <template v-else-if="actor.kind === 'workflow'">
      <GitBranch :class="size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0'" class="text-muted-foreground" />
      <span class="font-medium text-foreground">{{ actor.display_name }}</span>
    </template>

    <!-- system → cog + System badge -->
    <template v-else-if="actor.kind === 'system'">
      <Cog :class="size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0'" class="text-muted-foreground" />
      <Badge variant="secondary" class="h-5 px-1.5 text-[10px]">
        {{ actor.display_name || 'System' }}
      </Badge>
    </template>

    <!-- fallback (unknown kind) -->
    <template v-else>
      <span class="font-medium text-foreground">{{ actor.display_name }}</span>
    </template>
  </template>

  <!-- No actor → dash -->
  <template v-else>
    <span class="text-muted-foreground">—</span>
  </template>
</template>
