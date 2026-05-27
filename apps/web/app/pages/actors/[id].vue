<script setup lang="ts">
import { computed, ref, watchEffect } from 'vue'
import { Bot, Cog, Settings, User } from 'lucide-vue-next'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import type { ActorRef } from '~/types/actor'

const { t } = useI18n()
const route = useRoute()

const id = computed(() => String(route.params.id ?? ''))

useHead({ title: () => `${t('actor.title')} · ${id.value} - ${t('app.name')}` })

interface ActorDetail {
  actor: ActorRef
  issues_opened: number
  comments_authored: number
  contributions_pushed: number
}

const actor = ref<ActorDetail | null>(null)
const loading = ref(true)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const data = await $fetch<ActorDetail>(`/api/v1/actors/${encodeURIComponent(id.value)}`, {
      credentials: 'include',
    })
    actor.value = data
  } catch (e: any) {
    if (e?.response?.status === 404) {
      error.value = t('actor.notFound')
    } else {
      error.value = e?.data?.error ?? t('actor.loadFailed')
    }
  } finally {
    loading.value = false
  }
}

watchEffect(() => { if (id.value) load() })

function initialOf(s: string): string {
  return s ? s.charAt(0).toUpperCase() : '?'
}

function kindIcon(kind: string) {
  switch (kind) {
    case 'user': return User
    case 'agent':
    case 'agent_session':
    case 'agent_role':
    case 'bot': return Bot
    case 'workflow':
    case 'workflow_run': return Settings
    case 'system': return Cog
    default: return User
  }
}

function kindLabel(kind: string): string {
  const key = `actor.kinds.${kind}`
  // t() returns the key itself if not found; if it looks like a key path, fallback to kind
  const label = t(key)
  return label === key ? kind : label
}
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-6">
    <header class="space-y-2">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('actor.title') }}
      </h1>
    </header>

    <!-- Loading -->
    <Card v-if="loading" class="gap-0 py-0">
      <CardContent class="space-y-3 p-6">
        <Skeleton class="h-6 w-48" />
        <Skeleton class="h-4 w-32" />
        <Skeleton class="h-4 w-64" />
      </CardContent>
    </Card>

    <!-- Error -->
    <Card v-else-if="error" class="gap-0 py-0">
      <CardContent class="p-6 text-sm text-destructive">
        {{ error }}
      </CardContent>
    </Card>

    <!-- Actor detail -->
    <template v-else-if="actor">
      <!-- Identity card -->
      <Card class="gap-0 py-0">
        <CardHeader>
          <div class="flex items-center gap-3">
            <Avatar class="size-10 shrink-0">
              <AvatarFallback class="bg-primary/10 text-primary text-sm">
                <component
                  v-if="actor.actor.kind !== 'user'"
                  :is="kindIcon(actor.actor.kind)"
                  class="size-5"
                />
                <template v-else>
                  {{ initialOf(actor.actor.display_name) }}
                </template>
              </AvatarFallback>
            </Avatar>
            <div class="min-w-0">
              <CardTitle class="truncate text-lg">
                {{ actor.actor.display_name || '—' }}
              </CardTitle>
              <p class="text-xs text-muted-foreground font-mono">
                {{ actor.actor.id }}
              </p>
            </div>
            <Badge variant="secondary" class="ml-auto shrink-0">
              {{ kindLabel(actor.actor.kind) }}
            </Badge>
          </div>
        </CardHeader>
      </Card>

      <!-- Activity summary -->
      <Card class="gap-0 py-0">
        <CardHeader>
          <CardTitle class="text-base">{{ t('actor.activity') }}</CardTitle>
        </CardHeader>
        <CardContent class="grid grid-cols-3 gap-4 text-center">
          <div class="space-y-1">
            <p class="text-2xl font-semibold tabular-nums">{{ actor.issues_opened }}</p>
            <p class="text-xs text-muted-foreground">{{ t('actor.issuesOpened') }}</p>
          </div>
          <div class="space-y-1">
            <p class="text-2xl font-semibold tabular-nums">{{ actor.comments_authored }}</p>
            <p class="text-xs text-muted-foreground">{{ t('actor.commentsAuthored') }}</p>
          </div>
          <div class="space-y-1">
            <p class="text-2xl font-semibold tabular-nums">{{ actor.contributions_pushed }}</p>
            <p class="text-xs text-muted-foreground">{{ t('actor.contributionsPushed') }}</p>
          </div>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
