<script setup lang="ts">
import { computed } from 'vue'
import { Bot, Cog, GitBranch, Settings, User } from 'lucide-vue-next'
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

/**
 * Derive a route for the actor profile / detail page.
 * Returns null when the actor kind is not navigable (e.g. system, bot).
 */
const route = computed<string | null>(() => {
  const a = props.actor
  if (!a) return null

  // actor_id (DB primary key) is the preferred routing target
  if (a.actor_id) return `/actors/${a.actor_id}`

  // Parse the stable id field
  const kind = a.kind
  const id = a.id

  switch (kind) {
    case 'user': {
      const uid = a.user_id ?? id.split(':')[1]
      return uid ? `/users/${uid}` : null
    }
    case 'agent':
    case 'agent_role': {
      // /actors/<actor_id> when available, otherwise /actors/<full-id>
      return `/actors/${encodeURIComponent(id)}`
    }
    case 'agent_session': {
      // route to session detail
      const sessionId = id.startsWith('agent:session:') ? id.split(':')[2] : id
      return `/sessions/${sessionId}`
    }
    case 'workflow':
    case 'workflow_run': {
      const runId = a.workflow_run_id ?? (id.startsWith('workflow:run:') ? id.split(':')[2] : null)
      return runId ? `/runs/${runId}` : null
    }
    case 'system':
    case 'bot':
      return null
    default:
      // Unknown kinds: link to /actors if we have an id
      return id ? `/actors/${encodeURIComponent(id)}` : null
  }
})

/** Display label for agent-like kinds. */
const agentLabel = computed(() => {
  const a = props.actor
  if (!a) return ''
  if (a.role_key) return `@agent-${a.role_key}`
  return a.display_name || a.id
})
</script>

<template>
  <template v-if="actor">
    <!-- user → avatar + username, clickable -->
    <template v-if="actor.kind === 'user'">
      <NuxtLink
        v-if="route"
        :to="route"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
          <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
            {{ initialOf(actor.display_name) }}
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">{{ actor.display_name || '—' }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
          <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
            {{ initialOf(actor.display_name) }}
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">{{ actor.display_name || '—' }}</span>
      </span>
    </template>

    <!-- agent / agent_role → bot icon + @agent-role, clickable -->
    <template v-else-if="actor.kind === 'agent' || actor.kind === 'agent_role'">
      <NuxtLink
        v-if="route"
        :to="route"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
          <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
            <Bot :class="size === 'sm' ? 'size-2.5' : 'size-3'" />
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground" :title="agentLabel">
          {{ agentLabel }}
        </span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
          <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
            <Bot :class="size === 'sm' ? 'size-2.5' : 'size-3'" />
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground" :title="agentLabel">
          {{ agentLabel }}
        </span>
      </span>
    </template>

    <!-- agent_session → bot icon + @agent-role #session-N, clickable -->
    <template v-else-if="actor.kind === 'agent_session'">
      <NuxtLink
        v-if="route"
        :to="route"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
          <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
            <Bot :class="size === 'sm' ? 'size-2.5' : 'size-3'" />
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">{{ agentLabel }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0'">
          <AvatarFallback class="bg-primary/10 text-primary" :class="size === 'sm' ? 'text-[9px]' : 'text-[10px]'">
            <Bot :class="size === 'sm' ? 'size-2.5' : 'size-3'" />
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">{{ agentLabel }}</span>
      </span>
    </template>

    <!-- workflow / workflow_run → branch/gear icon + name, clickable -->
    <template v-else-if="actor.kind === 'workflow' || actor.kind === 'workflow_run'">
      <NuxtLink
        v-if="route"
        :to="route"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Settings :class="size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0'" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Settings :class="size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0'" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
      </span>
    </template>

    <!-- system → cog + System badge, not clickable -->
    <template v-else-if="actor.kind === 'system'">
      <span class="inline-flex items-center gap-1.5">
        <Cog :class="size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0'" class="text-muted-foreground" />
        <Badge variant="secondary" class="h-5 px-1.5 text-[10px]">
          {{ actor.display_name || 'System' }}
        </Badge>
      </span>
    </template>

    <!-- bot → robot icon + handle, not clickable per spec -->
    <template v-else-if="actor.kind === 'bot'">
      <span class="inline-flex items-center gap-1.5">
        <Bot :class="size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0'" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
      </span>
    </template>

    <!-- fallback (unknown kind) -->
    <template v-else>
      <NuxtLink
        v-if="route"
        :to="route"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
      </NuxtLink>
      <span v-else class="font-medium text-foreground">{{ actor.display_name }}</span>
    </template>
  </template>

  <!-- No actor → dash -->
  <template v-else>
    <span class="text-muted-foreground">—</span>
  </template>
</template>
