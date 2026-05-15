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
import type { Crumb } from '~/composables/useBreadcrumbs'

const { locale, locales, setLocale, t } = useI18n()
const route = useRoute()
const pageCrumbs = useBreadcrumbsState()

// Pages register their own crumbs by calling setBreadcrumbs() in setup.
// Until a page does, fall back to the raw path so the gap is visible
// during dev rather than producing an empty header.
const crumbs = computed<Crumb[]>(() => {
  if (pageCrumbs.value.length > 0) return pageCrumbs.value
  return [{ label: route.path }]
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
