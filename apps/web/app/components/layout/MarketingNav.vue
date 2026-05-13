<script setup lang="ts">
import { computed } from 'vue'
import { Check, GitBranch, Languages } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

const { t, locale, locales, setLocale } = useI18n()
const { user } = useCurrentUser()
const availableLocales = computed(() => (locales as any).value as { code: string; name: string }[])
</script>

<template>
  <header class="sticky top-0 z-30 border-b bg-background/80 backdrop-blur">
    <div class="mx-auto flex h-14 max-w-6xl items-center justify-between px-6">
      <NuxtLink to="/" class="flex items-center gap-2 font-semibold tracking-tight">
        <span class="grid size-7 place-items-center rounded-md bg-primary text-primary-foreground">
          <GitBranch class="size-4" />
        </span>
        {{ t('app.name') }}
      </NuxtLink>
      <div class="flex items-center gap-2">
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
        <Button v-if="user" size="sm" as-child>
          <NuxtLink to="/">
            {{ t('nav.dashboard') }}
          </NuxtLink>
        </Button>
        <Button v-else size="sm" as-child>
          <NuxtLink to="/login">
            {{ t('login.submit') }}
          </NuxtLink>
        </Button>
      </div>
    </div>
  </header>
</template>
