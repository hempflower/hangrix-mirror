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

const avatarClasses = computed(() =>
  props.size === 'sm' ? 'size-5 shrink-0' : 'size-6 shrink-0',
)
const fallbackTextClass = computed(() =>
  props.size === 'sm' ? 'text-[9px]' : 'text-[10px]',
)
const iconClass = computed(() =>
  props.size === 'sm' ? 'size-2.5' : 'size-3',
)
const inlineIconClass = computed(() =>
  props.size === 'sm' ? 'size-3 shrink-0' : 'size-3.5 shrink-0',
)

function initialOf(s: string): string {
  return s ? s.charAt(0).toUpperCase() : '?'
}

/** Derive a route for the actor, or null if not clickable. */
function actorRoute(
  actor: ActorRef,
): { to: string; external?: boolean } | null {
  switch (actor.kind) {
    case 'user':
      // Route to user profile. If user_id is present use it; otherwise fall
      // back to display_name as a slug (matches existing /users/:id patterns).
      if (actor.user_id) return { to: `/users/${actor.user_id}` }
      return { to: `/users/${actor.display_name}` }
    case 'agent_session':
      if (actor.session_id) return { to: `/sessions/${actor.session_id}` }
      return null
    case 'agent_role':
      if (actor.actor_id) return { to: `/actors/${actor.actor_id}` }
      // Fallback: route by role_key if actor_id not yet populated (pre-backfill).
      if (actor.role_key) return { to: `/actors/${actor.role_key}` }
      return null
    case 'workflow_run':
      if (actor.workflow_run_id) return { to: `/runs/${actor.workflow_run_id}` }
      return null
    // Legacy kinds — best-effort routing
    case 'agent':
      if (actor.role_key) return { to: `/actors/${actor.role_key}` }
      return null
    case 'workflow':
      if (actor.workflow_run_id) return { to: `/runs/${actor.workflow_run_id}` }
      return null
    case 'system':
      return null // not clickable
    case 'bot':
      return null // no dedicated bot page yet
    default:
      return null
  }
}

const route = computed(() => {
  if (!props.actor) return null
  return actorRoute(props.actor)
})
</script>

<template>
  <template v-if="actor">
    <!-- user → avatar + username, linked -->
    <template v-if="actor.kind === 'user'">
      <NuxtLink
        v-if="route"
        :to="route.to"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            {{ initialOf(actor.display_name) }}
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">{{ actor.display_name || '—' }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            {{ initialOf(actor.display_name) }}
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">{{ actor.display_name || '—' }}</span>
      </span>
    </template>

    <!-- agent (legacy) → bot icon + @agent-role -->
    <template v-else-if="actor.kind === 'agent'">
      <NuxtLink
        v-if="route"
        :to="route.to"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            <Bot :class="iconClass" />
          </AvatarFallback>
        </Avatar>
        <span
          class="font-medium text-foreground"
          :title="`@agent-${actor.role_key || actor.display_name}`"
        >
          {{ actor.display_name }}
        </span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            <Bot :class="iconClass" />
          </AvatarFallback>
        </Avatar>
        <span
          class="font-medium text-foreground"
          :title="`@agent-${actor.role_key || actor.display_name}`"
        >
          {{ actor.display_name }}
        </span>
      </span>
    </template>

    <!-- agent_session → role icon + @agent-<role> #session-<id> -->
    <template v-else-if="actor.kind === 'agent_session'">
      <NuxtLink
        v-if="route"
        :to="route.to"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            <Bot :class="iconClass" />
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">
          {{ actor.display_name }}
        </span>
        <span
          v-if="actor.session_id"
          class="text-muted-foreground"
        >#session-{{ actor.session_id }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            <Bot :class="iconClass" />
          </AvatarFallback>
        </Avatar>
        <span class="font-medium text-foreground">
          {{ actor.display_name }}
        </span>
        <span
          v-if="actor.session_id"
          class="text-muted-foreground"
        >#session-{{ actor.session_id }}</span>
      </span>
    </template>

    <!-- agent_role → role icon + @agent-<role> -->
    <template v-else-if="actor.kind === 'agent_role'">
      <NuxtLink
        v-if="route"
        :to="route.to"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            <Bot :class="iconClass" />
          </AvatarFallback>
        </Avatar>
        <span
          class="font-medium text-foreground"
          :title="`@agent-${actor.role_key || actor.display_name}`"
        >
          {{ actor.display_name }}
        </span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <Avatar :class="avatarClasses">
          <AvatarFallback
            class="bg-primary/10 text-primary"
            :class="fallbackTextClass"
          >
            <Bot :class="iconClass" />
          </AvatarFallback>
        </Avatar>
        <span
          class="font-medium text-foreground"
          :title="`@agent-${actor.role_key || actor.display_name}`"
        >
          {{ actor.display_name }}
        </span>
      </span>
    </template>

    <!-- workflow (legacy) → gear icon + name -->
    <template v-else-if="actor.kind === 'workflow'">
      <NuxtLink
        v-if="route"
        :to="route.to"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <GitBranch :class="inlineIconClass" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <GitBranch :class="inlineIconClass" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
      </span>
    </template>

    <!-- workflow_run → gear icon + workflow/<name>#<run> -->
    <template v-else-if="actor.kind === 'workflow_run'">
      <NuxtLink
        v-if="route"
        :to="route.to"
        class="inline-flex items-center gap-1.5 hover:underline"
      >
        <GitBranch :class="inlineIconClass" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
        <span
          v-if="actor.workflow_run_id"
          class="text-muted-foreground"
        >#{{ actor.workflow_run_id }}</span>
      </NuxtLink>
      <span v-else class="inline-flex items-center gap-1.5">
        <GitBranch :class="inlineIconClass" class="text-muted-foreground" />
        <span class="font-medium text-foreground">{{ actor.display_name }}</span>
        <span
          v-if="actor.workflow_run_id"
          class="text-muted-foreground"
        >#{{ actor.workflow_run_id }}</span>
      </span>
    </template>

    <!-- system → grey "System" label, not clickable -->
    <template v-else-if="actor.kind === 'system'">
      <Cog :class="inlineIconClass" class="text-muted-foreground" />
      <Badge variant="secondary" class="h-5 px-1.5 text-[10px]">
        {{ actor.display_name || 'System' }}
      </Badge>
    </template>

    <!-- bot → robot icon + handle -->
    <template v-else-if="actor.kind === 'bot'">
      <Avatar :class="avatarClasses">
        <AvatarFallback
          class="bg-primary/10 text-primary"
          :class="fallbackTextClass"
        >
          <Bot :class="iconClass" />
        </AvatarFallback>
      </Avatar>
      <span class="font-medium text-foreground">{{ actor.display_name }}</span>
    </template>

    <!-- fallback (unknown / future kind) -->
    <template v-else>
      <span class="font-medium text-foreground">{{ actor.display_name }}</span>
    </template>
  </template>

  <!-- No actor → dash -->
  <template v-else>
    <span class="text-muted-foreground">—</span>
  </template>
</template>
