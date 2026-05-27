<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import ActorAvatar from '@/components/ActorAvatar.vue'
import type { ActorRef } from '~/types/actor'

definePageMeta({ layout: 'default' })

const { t } = useI18n()
const route = useRoute()

const id = computed(() => String(route.params.id ?? ''))

const actor = ref<{
  ref: ActorRef
  display_name: string
  kind: string
  // Activity summary
  recent_issues?: Array<{ number: number; title: string }>
  recent_comments?: Array<{ issue_number: number; body_preview: string }>
  recent_contributions?: Array<{ id: number; title: string; ref_name: string }>
} | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  if (!id.value) return
  loading.value = true
  error.value = null
  try {
    const data = await $fetch(`/api/v1/actors/${id.value}`, {
      credentials: 'include',
    })
    actor.value = data as any
  } catch (e: any) {
    if (e?.response?.status === 404) {
      error.value = t('actor.notFound')
    } else {
      error.value = e?.data?.error ?? t('actor.loadFailed')
    }
    actor.value = null
  } finally {
    loading.value = false
  }
}

watch(id, () => { load() }, { immediate: true })
</script>

<template>
  <div class="mx-auto max-w-3xl space-y-6 py-8">
    <!-- Loading -->
    <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <!-- Error -->
    <Card v-else-if="error">
      <CardContent class="py-6 text-center">
        <p class="text-sm text-destructive">{{ error }}</p>
      </CardContent>
    </Card>

    <!-- Actor profile -->
    <template v-else-if="actor">
      <Card>
        <CardHeader>
          <div class="flex items-center gap-3">
            <ActorAvatar :actor="actor.ref" size="default" />
            <Badge variant="outline">{{ actor.kind }}</Badge>
          </div>
        </CardHeader>
        <CardContent class="space-y-4">
          <div>
            <p class="text-sm text-muted-foreground">{{ t('actor.displayName') }}</p>
            <p class="font-medium">{{ actor.display_name }}</p>
          </div>
        </CardContent>
      </Card>

      <!-- Recent activity -->
      <Card v-if="actor.recent_issues?.length">
        <CardHeader>
          <CardTitle class="text-base">{{ t('actor.recentIssues') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <ul class="space-y-1">
            <li v-for="iss in actor.recent_issues" :key="iss.number" class="text-sm">
              <NuxtLink
                :to="`/issues/${iss.number}`"
                class="text-primary hover:underline"
              >
                #{{ iss.number }} {{ iss.title }}
              </NuxtLink>
            </li>
          </ul>
        </CardContent>
      </Card>

      <Card v-if="actor.recent_contributions?.length">
        <CardHeader>
          <CardTitle class="text-base">{{ t('actor.recentContributions') }}</CardTitle>
        </CardHeader>
        <CardContent>
          <ul class="space-y-1">
            <li v-for="c in actor.recent_contributions" :key="c.id" class="text-sm">
              <span class="font-medium">{{ c.title || c.ref_name }}</span>
            </li>
          </ul>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
