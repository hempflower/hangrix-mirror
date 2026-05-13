<script setup lang="ts">
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import RepoSidebar from '@/components/layout/RepoSidebar.vue'
import AppHeader from '@/components/layout/AppHeader.vue'
</script>

<template>
  <SidebarProvider>
    <RepoSidebar />
    <SidebarInset>
      <AppHeader />
      <div class="flex flex-1 flex-col gap-4 p-4 md:p-6">
        <slot />
      </div>
    </SidebarInset>
  </SidebarProvider>
</template>
