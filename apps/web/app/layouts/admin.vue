<script setup lang="ts">
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import AdminSidebar from '@/components/layout/AdminSidebar.vue'
import AppHeader from '@/components/layout/AppHeader.vue'
</script>

<template>
  <SidebarProvider>
    <AdminSidebar />
    <SidebarInset>
      <AppHeader />
      <div class="flex flex-1 flex-col gap-4 p-4 md:p-6">
        <slot />
      </div>
    </SidebarInset>
  </SidebarProvider>
</template>
