<script setup lang="ts">
import MarketingNav from '@/components/layout/MarketingNav.vue'
</script>

<template>
  <div class="min-h-screen bg-background text-foreground">
    <MarketingNav />
    <main class="mx-auto max-w-6xl px-6 py-10">
      <slot />
    </main>
  </div>
</template>
