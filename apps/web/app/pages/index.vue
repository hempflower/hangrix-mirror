<script setup lang="ts">
import { computed, ref } from 'vue'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'

const config = useRuntimeConfig()
const { data, error, refresh, status } = await useFetch<{ message: string }>('/api/hello', {
  baseURL: config.public.apiBase,
  server: false,
})

const name = ref('hangrix')
const greeting = computed(() => `hello, ${name.value}`)
</script>

<template>
  <main class="min-h-screen bg-background text-foreground">
    <div class="mx-auto max-w-3xl space-y-8 p-8">
      <header class="space-y-2">
        <h1 class="text-3xl font-semibold tracking-tight">
          {{ greeting }}
        </h1>
        <p class="text-muted-foreground">
          Single Go binary serving the embedded Nuxt SPA + JSON API.
        </p>
        <div class="flex gap-2">
          <Badge>Go {{ '1.26.1' }}</Badge>
          <Badge variant="secondary">Nuxt 4</Badge>
          <Badge variant="outline">shadcn-vue</Badge>
          <Badge variant="destructive">embedded</Badge>
        </div>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>API ping</CardTitle>
          <CardDescription>GET /api/hello — handled by the chi router inside the same binary.</CardDescription>
        </CardHeader>
        <CardContent class="space-y-2">
          <p v-if="status === 'pending'" class="text-sm text-muted-foreground">
            Loading…
          </p>
          <p v-else-if="error" class="text-sm text-destructive">
            {{ error.message }}
          </p>
          <p v-else-if="data" class="font-mono text-sm">
            {{ data.message }}
          </p>
        </CardContent>
        <CardFooter>
          <Button @click="refresh()">
            Refresh
          </Button>
        </CardFooter>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Two-way binding</CardTitle>
          <CardDescription>shadcn-vue Input with a reactive Vue ref.</CardDescription>
        </CardHeader>
        <CardContent class="space-y-3">
          <Input v-model="name" placeholder="Type a name…" />
          <p class="text-sm">
            Output:
            <span class="font-mono">{{ greeting }}</span>
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Button variants</CardTitle>
          <CardDescription>Sanity-check shadcn-vue + Tailwind v4 theming.</CardDescription>
        </CardHeader>
        <CardContent class="flex flex-wrap gap-2">
          <Button variant="default">
            default
          </Button>
          <Button variant="secondary">
            secondary
          </Button>
          <Button variant="outline">
            outline
          </Button>
          <Button variant="ghost">
            ghost
          </Button>
          <Button variant="link">
            link
          </Button>
          <Button variant="destructive">
            destructive
          </Button>
        </CardContent>
      </Card>
    </div>
  </main>
</template>
