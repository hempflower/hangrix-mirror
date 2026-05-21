<script setup lang="ts">
import { computed, onMounted } from 'vue'
import {
  ArrowLeft,
  BookOpen,
  CircleDot,
  Code,
  Diff,
  GitBranch,
  LogOut,
  Play,
  Rocket,
  Settings,
  Shield,
  Tag,
  User,
} from 'lucide-vue-next'
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

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user, logout } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

const { repo, load } = useRepo(() => owner.value, () => name.value)
const { emptyRepo, load: loadRefs } = useRepoRefs(() => owner.value, () => name.value)

onMounted(() => {
  if (owner.value && name.value) {
    load()
    loadRefs()
  }
})

watch([owner, name], ([o, n]) => {
  if (o && n) {
    load(true)
    loadRefs(true)
  }
})

const repoBase = computed(() => `/${owner.value}/${name.value}`)

const canManage = computed(() => repo.value?.viewer_permission === 'manage')

interface NavItem {
  key: string
  to: string
  icon: any
  label: string
  exact?: boolean
}

const repoItems = computed<NavItem[]>(() => {
  const base = repoBase.value
  const items: NavItem[] = [
    { key: 'code', to: base, icon: Code, label: t('repo.nav.code'), exact: true },
  ]
  // Issues are available even on an empty repo — opening a placeholder
  // issue is a perfectly valid first action.
  items.push({ key: 'issues', to: `${base}/issues`, icon: CircleDot, label: t('repo.tabs2.issues') })
  // On an empty repo there are no refs to browse, nothing to compare, and
  // no useful settings — collapse the nav to issues only until the first
  // push lands.
  if (emptyRepo.value) {
    return items
  }
  items.push(
    { key: 'branches', to: `${base}/branches`, icon: GitBranch, label: t('repo.tabs.branches') },
    { key: 'tags', to: `${base}/tags`, icon: Tag, label: t('repo.tabs.tags') },
  { key: 'releases', to: `${base}/releases`, icon: Rocket, label: t('repo.tabs2.releases') },
  { key: 'workflows', to: `${base}/workflows`, icon: Play, label: t('repo.workflows.tabLabel') },
    { key: 'compare', to: `${base}/compare`, icon: Diff, label: t('repo.tabs.compare') },
  )
  if (canManage.value) {
    items.push({ key: 'settings', to: `${base}/settings`, icon: Settings, label: t('repo.settingsLink') })
  }
  return items
})

function isActive(item: NavItem) {
  if (item.key === 'code') {
    // Code is the repo root and includes anything tree- / blob- / commit-detail-related;
    // the page-level Tabs widget owns files-vs-commits switching from there.
    if (route.path === repoBase.value) return true
    const prefix = `${repoBase.value}/`
    if (route.path.startsWith(`${prefix}blob`)) return true
    if (route.path.startsWith(`${prefix}commits/`)) return true
    return false
  }
  const targetPath = item.to.split('?')[0]
  return route.path === targetPath || route.path.startsWith(`${targetPath}/`)
}

async function onLogout() {
  await logout()
  router.push('/login')
}

const userInitial = computed(() => user.value?.username?.charAt(0).toUpperCase() ?? '?')
const repoInitial = computed(() => name.value.charAt(0).toUpperCase() || '?')
</script>

<template>
  <Sidebar variant="inset" collapsible="icon">
    <SidebarHeader>
      <SidebarMenu>
        <SidebarMenuItem>
          <SidebarMenuButton size="lg" as-child>
            <NuxtLink :to="repoBase">
              <div class="flex aspect-square size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <BookOpen class="size-4" />
              </div>
              <div class="flex flex-1 items-center gap-2 group-data-[collapsible=icon]:hidden">
                <div class="grid flex-1 text-left text-sm leading-tight min-w-0">
                  <span class="truncate text-xs text-muted-foreground">{{ owner }}</span>
                  <span class="truncate font-semibold">{{ name }}</span>
                </div>
                <Badge
                  v-if="repo"
                  :variant="repo.visibility === 'private' ? 'outline' : 'secondary'"
                  class="shrink-0"
                >
                  {{ t(`repo.visibility${repo.visibility === 'private' ? 'Private' : 'Public'}`) }}
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
        <SidebarGroupLabel>{{ t('repo.sidebarGroup') }}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in repoItems" :key="item.key">
              <SidebarMenuButton :is-active="isActive(item)" :tooltip="item.label" as-child>
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
          <SidebarMenuButton :tooltip="t('nav.backToWorkspace')" as-child>
            <NuxtLink to="/">
              <ArrowLeft />
              <span>{{ t('nav.backToWorkspace') }}</span>
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
              <DropdownMenuItem v-if="user.role === 'admin'" as-child>
                <NuxtLink to="/admin/users" class="cursor-pointer">
                  <Shield />
                  <span>{{ t('admin.section') }}</span>
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
