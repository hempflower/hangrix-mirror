<script setup lang="ts">
import { computed } from 'vue'
import type { NuxtError } from '#app'
import { Button } from '@/components/ui/button'

const props = defineProps<{ error: NuxtError }>()

const { t } = useI18n()

const status = computed(() => props.error?.statusCode ?? 500)
const message = computed(() => props.error?.statusMessage || props.error?.message || t('errors.unknown'))

function goHome() {
  clearError({ redirect: '/' })
}
</script>

<template>
  <div class="grid min-h-screen place-items-center bg-background p-6 text-foreground">
    <div class="max-w-md space-y-4 text-center">
      <p class="font-mono text-6xl font-semibold tracking-tighter text-muted-foreground">
        {{ status }}
      </p>
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ message }}
      </h1>
      <Button @click="goHome">
        {{ t('nav.home') }}
      </Button>
    </div>
  </div>
</template>
