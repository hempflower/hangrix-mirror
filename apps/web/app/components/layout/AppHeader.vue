<script setup lang="ts">
import { computed } from 'vue'
import { Check, Languages } from 'lucide-vue-next'
import { SidebarTrigger } from '@/components/ui/sidebar'
import { Separator } from '@/components/ui/separator'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'

const { t, locale, locales, setLocale } = useI18n()
const route = useRoute()

interface Crumb { label: string; to?: string }

function shortSha(s: string) {
  return s.slice(0, 7)
}

const crumbs = computed<Crumb[]>(() => {
  const path = route.path

  if (path === '/') {
    return [{ label: t('nav.dashboard') }]
  }
  if (path.startsWith('/admin/users')) {
    return [
      { label: t('admin.section'), to: '/admin/users' },
      { label: t('admin.users.title') },
    ]
  }
  if (path.startsWith('/profile')) {
    return [{ label: t('nav.profile') }]
  }
  if (path === '/repos' || path.startsWith('/repos/')) {
    return [{ label: t('repo.title') }]
  }

  // Repo paths: /[owner]/[name][/...]
  const owner = String(route.params.owner ?? '')
  const name = String(route.params.name ?? '')
  if (owner && name) {
    const base = `/${owner}/${name}`
    const head: Crumb[] = [
      { label: owner, to: base },
      { label: name, to: base },
    ]
    // /[owner]/[name]
    if (path === base) {
      return [head[0]!, { label: name }]
    }
    // /[owner]/[name]/blob/<ref>/<...path>
    if (path.startsWith(`${base}/blob/`)) {
      const rawPath = route.params.path
      const segs = Array.isArray(rawPath)
        ? (rawPath as string[]).filter(Boolean)
        : String(rawPath ?? '').split('/').filter(Boolean)
      const out: Crumb[] = [head[0]!, head[1]!, { label: 'blob' }]
      segs.forEach(seg => out.push({ label: seg }))
      return out
    }
    // /[owner]/[name]/commits/[sha]
    if (path.startsWith(`${base}/commits/`)) {
      const sha = String(route.params.sha ?? '')
      return [head[0]!, head[1]!, { label: t('repo.tabs.commits'), to: `${base}?tab=commits` }, { label: shortSha(sha) }]
    }
    if (path === `${base}/branches`) {
      return [head[0]!, head[1]!, { label: t('repo.tabs.branches') }]
    }
    if (path === `${base}/tags`) {
      return [head[0]!, head[1]!, { label: t('repo.tabs.tags') }]
    }
    if (path === `${base}/compare`) {
      return [head[0]!, head[1]!, { label: t('repo.tabs.compare') }]
    }
    if (path === `${base}/settings`) {
      return [head[0]!, head[1]!, { label: t('repo.settingsLink') }]
    }
    return [head[0]!, { label: name }]
  }

  return [{ label: path }]
})

const availableLocales = computed(() => (locales as any).value as { code: string; name: string }[])
</script>

<template>
  <header class="flex h-14 shrink-0 items-center gap-2 border-b px-4">
    <SidebarTrigger class="-ml-1" />
    <Separator orientation="vertical" class="mr-2 h-4" />

    <Breadcrumb>
      <BreadcrumbList>
        <template v-for="(c, i) in crumbs" :key="i">
          <BreadcrumbItem>
            <BreadcrumbLink v-if="c.to && i < crumbs.length - 1" as-child>
              <NuxtLink :to="c.to">
                {{ c.label }}
              </NuxtLink>
            </BreadcrumbLink>
            <BreadcrumbPage v-else>
              {{ c.label }}
            </BreadcrumbPage>
          </BreadcrumbItem>
          <BreadcrumbSeparator v-if="i < crumbs.length - 1" />
        </template>
      </BreadcrumbList>
    </Breadcrumb>

    <div class="ml-auto flex items-center gap-2">
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
          <Button variant="ghost" size="icon" :aria-label="t('sidebar.language')">
            <Languages class="size-4" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" class="w-40">
          <DropdownMenuLabel>{{ t('sidebar.language') }}</DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            v-for="l in availableLocales"
            :key="l.code"
            class="cursor-pointer"
            @click="setLocale(l.code as any)"
          >
            <Check :class="['size-4', l.code === locale ? 'opacity-100' : 'opacity-0']" />
            <span>{{ l.name }}</span>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  </header>
</template>
