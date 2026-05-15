<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { Building2, FolderGit2, GitBranch, LayoutDashboard, LogOut, Plus, Settings, Shield, User } from 'lucide-vue-next'
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
  icon: typeof LayoutDashboard
  label: string
}

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user, logout } = useCurrentUser()
const { orgs: myOrgs, refresh: refreshMyOrgs } = useMyOrgs()

onMounted(async () => {
  if (user.value && !myOrgs.value) await refreshMyOrgs()
})

async function onLogout() {
  await logout()
  router.push('/login')
}

const workspaceItems = computed<NavItem[]>(() => [
  { key: 'dashboard', to: '/', icon: LayoutDashboard, label: t('nav.dashboard') },
  { key: 'repos', to: '/repos', icon: FolderGit2, label: t('nav.repos') },
])

const accountItems = computed<NavItem[]>(() => [
  { key: 'profile', to: '/profile', icon: User, label: t('nav.profile') },
])

function isActive(to: string) {
  if (to === '/') return route.path === '/'
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
            <NuxtLink to="/">
              <div class="flex aspect-square size-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                <GitBranch class="size-4" />
              </div>
              <div class="grid flex-1 text-left text-sm leading-tight group-data-[collapsible=icon]:hidden">
                <span class="truncate font-semibold">{{ t('app.name') }}</span>
                <span class="truncate text-xs text-muted-foreground">{{ t('app.tagline') }}</span>
              </div>
            </NuxtLink>
          </SidebarMenuButton>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarHeader>

    <SidebarContent>
      <SidebarGroup>
        <SidebarGroupLabel>{{ t('sidebar.workspace') }}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in workspaceItems" :key="item.key">
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

      <SidebarGroup>
        <SidebarGroupLabel class="flex items-center justify-between">
          <span>{{ t('sidebar.organizations') }}</span>
          <NuxtLink to="/orgs/new" :title="t('org.create')" class="rounded p-0.5 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground">
            <Plus class="size-3.5" />
          </NuxtLink>
        </SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="o in myOrgs ?? []" :key="o.id">
              <SidebarMenuButton :is-active="route.path === `/${o.name}`" :tooltip="o.name" as-child>
                <NuxtLink :to="`/${o.name}`">
                  <Building2 />
                  <span>{{ o.display_name || o.name }}</span>
                </NuxtLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <SidebarMenuItem v-if="(myOrgs ?? []).length === 0">
              <SidebarMenuButton :tooltip="t('org.create')" as-child>
                <NuxtLink to="/orgs/new">
                  <Plus />
                  <span>{{ t('org.create') }}</span>
                </NuxtLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>

      <SidebarGroup>
        <SidebarGroupLabel>{{ t('sidebar.account') }}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in accountItems" :key="item.key">
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
                <Badge v-if="user.role === 'admin'" variant="secondary">
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
                <NuxtLink to="/orgs/new" class="cursor-pointer">
                  <Building2 />
                  <span>{{ t('org.create') }}</span>
                </NuxtLink>
              </DropdownMenuItem>
              <DropdownMenuItem v-if="user.role === 'admin'" as-child>
                <NuxtLink to="/admin/users" class="cursor-pointer">
                  <Shield />
                  <span>{{ t('admin.section') }}</span>
                </NuxtLink>
              </DropdownMenuItem>
              <DropdownMenuItem disabled>
                <Settings />
                <span>{{ t('nav.settings') }}</span>
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
