<script setup lang="ts">
import { Check, Languages } from 'lucide-vue-next'
import { computed } from 'vue'
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
const availableLocales = computed(() => (locales as any).value as { code: string; name: string }[])
</script>

<template>
  <div class="relative grid min-h-screen place-items-center bg-background p-6">
    <div class="absolute right-4 top-4">
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
    <slot />
  </div>
</template>
