<script setup lang="ts">
import { computed } from 'vue'
import { Activity, ArrowLeft, Bot, LogOut, Server, Shield, Sparkles, User, Users } from 'lucide-vue-next'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarSeparator,
} from '@/components/ui/sidebar'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'

interface NavItem {
  key: string
  to: string
  icon: typeof Users
  label: string
}

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user, logout } = useCurrentUser()

async function onLogout() {
  await logout()
  router.push('/login')
}

const manageItems = computed<NavItem[]>(() => [
  { key: 'users', to: '/admin/users', icon: Users, label: t('nav.users') },
  { key: 'llm', to: '/admin/llm', icon: Sparkles, label: t('nav.llmProviders') },
  { key: 'runners', to: '/admin/runners', icon: Server, label: t('nav.runners') },
  { key: 'usage', to: '/admin/llm-usage', icon: Activity, label: t('nav.llmUsage') },
  { key: 'agentSessions', to: '/admin/agent-sessions', icon: Bot, label: t('nav.agentSessions') },
])

function isActive(to: string) {
  return route.path === to || route.path.startsWith(`${to}/`)
}

const userInitial = computed(() => user.value?.username?.charAt(0).toUpperCase() ?? '?')
</script>

<template>
  <Sidebar variant="inset" collapsible="icon">
    <SidebarHeader>
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton size="lg" as-child>
            <NuxtLink to="/admin/users">
              <div class="flex aspect-square size-8 shrink-0 items-center justify-center rounded-lg bg-amber-500/15 text-amber-500">
                <Shield class="size-4" />
              </div>
              <div class="flex flex-1 items-center gap-2 group-data-[collapsible=icon]:hidden">
                <div class="grid flex-1 text-left text-sm leading-tight">
                  <span class="truncate font-semibold">{{ t('app.name') }}</span>
                  <span class="truncate text-xs text-muted-foreground">{{ t('admin.section') }}</span>
                </div>
                <Badge variant="outline" class="border-amber-500/40 text-amber-500">
                  {{ t('admin.badge') }}
                </Badge>
              </div>
            </NuxtLink>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarHeader>

    <SidebarSeparator />

    <SidebarContent>
      <SidebarGroup>
        <SidebarGroupLabel>{{ t('sidebar.manage') }}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in manageItems" :key="item.key">
              <SidebarMenuButton :is-active="isActive(item.to)" :tooltip="item.label" as-child>
                <NuxtLink :to="item.to">
                  <component :is="item.icon" />
                  <span>{{ item.label }}</span>
                </NuxtLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>
    </SidebarContent>

    <SidebarFooter>
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton :tooltip="t('admin.exit')" as-child>
            <NuxtLink to="/">
              <ArrowLeft />
              <span>{{ t('admin.exit') }}</span>
            </NuxtLink>
          </SidebarMenuButton>
        </SidebarMenuItem>

        <SidebarMenuItem v-if="user">
          <DropdownMenu>
            <DropdownMenuTrigger as-child>
              <SidebarMenuButton
                size="lg"
                class="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
              >
                <Avatar class="size-8 shrink-0 rounded-lg">
                  <AvatarFallback class="rounded-lg bg-primary/10 text-primary">
                    {{ userInitial }}
                  </AvatarFallback>
                </Avatar>
                <div class="grid flex-1 text-left text-sm leading-tight group-data-[collapsible=icon]:hidden">
                  <span class="truncate font-medium">{{ user.username }}</span>
                  <span class="truncate text-xs text-muted-foreground">{{ user.email }}</span>
                </div>
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              class="w-56"
              side="right"
              align="end"
              :side-offset="4"
            >
              <DropdownMenuLabel class="flex items-center gap-2 font-normal">
                <div class="grid flex-1 text-left text-sm leading-tight">
                  <span class="truncate font-medium">{{ user.username }}</span>
                  <span class="truncate text-xs text-muted-foreground">{{ user.email }}</span>
                </div>
                <Badge variant="secondary">
                  {{ t('role.admin') }}
                </Badge>
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem as-child>
                <NuxtLink to="/profile" class="cursor-pointer">
                  <User />
                  <span>{{ t('nav.profile') }}</span>
                </NuxtLink>
              </DropdownMenuItem>
              <DropdownMenuItem as-child>
                <NuxtLink to="/" class="cursor-pointer">
                  <ArrowLeft />
                  <span>{{ t('admin.exit') }}</span>
                </NuxtLink>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem class="cursor-pointer" @click="onLogout">
                <LogOut />
                <span>{{ t('nav.logout') }}</span>
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarFooter>
  </Sidebar>
</template>
