<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { AlertTriangle } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { PublicRepo, RepoRefs } from '~/types/repo'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { user, refresh: refreshUser } = useCurrentUser()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

const repo = ref<PublicRepo | null>(null)
const refs = ref<RepoRefs | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)

const generalMsg = ref<{ kind: 'ok' | 'err'; text: string } | null>(null)

const deleteOpen = ref(false)
const deleteConfirmInput = ref('')
const deleteError = ref<string | null>(null)
const deleting = ref(false)

const branches = computed(() => refs.value?.branches ?? [])
const fullName = computed(() => `${owner.value}/${name.value}`)
const canManage = computed(() => {
  if (!repo.value || !user.value) return false
  return user.value.role === 'admin' || user.value.id === repo.value.owner_id
})

const schema = computed(() => toTypedSchema(z.object({
  description: z.string().max(500).optional(),
  visibility: z.enum(['public', 'private']),
  default_branch: z.string().min(1),
})))

const initial = ref({
  description: '',
  visibility: 'private' as 'public' | 'private',
  default_branch: '',
})

async function load() {
  loading.value = true
  loadError.value = null
  try {
    if (!user.value) await refreshUser()
    repo.value = await $fetch<PublicRepo>(`/api/repos/${owner.value}/${name.value}`, {
      credentials: 'include',
    })
    refs.value = await $fetch<RepoRefs>(`/api/repos/${owner.value}/${name.value}/refs`, {
      credentials: 'include',
    })
    if (repo.value) {
      initial.value = {
        description: repo.value.description ?? '',
        visibility: repo.value.visibility,
        default_branch: repo.value.default_branch,
      }
    }
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('repo.notFound')
  } finally {
    loading.value = false
  }
}

watch([repo, user], () => {
  if (repo.value && user.value && !canManage.value) {
    router.replace(`/${owner.value}/${name.value}`)
  }
})

async function onSaveGeneral(values: any) {
  generalMsg.value = null
  try {
    const updated = await $fetch<PublicRepo>(`/api/repos/${owner.value}/${name.value}`, {
      method: 'PATCH',
      credentials: 'include',
      body: {
        description: values.description ?? '',
        visibility: values.visibility,
        default_branch: values.default_branch,
      },
    })
    repo.value = updated
    initial.value = {
      description: updated.description ?? '',
      visibility: updated.visibility,
      default_branch: updated.default_branch,
    }
    generalMsg.value = { kind: 'ok', text: t('repo.settings.saved') }
  } catch (e: any) {
    generalMsg.value = { kind: 'err', text: e?.data?.error ?? t('repo.settings.saveFailed') }
  }
}

async function onConfirmDelete() {
  if (deleteConfirmInput.value !== fullName.value) return
  deleting.value = true
  deleteError.value = null
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    router.push('/repos')
  } catch (e: any) {
    deleteError.value = e?.data?.error ?? t('repo.settings.deleteFailed')
  } finally {
    deleting.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="mx-auto w-full max-w-3xl space-y-6">
    <header class="space-y-2">
      <nav class="text-sm text-muted-foreground">
        <NuxtLink :to="`/${owner}/${name}`" class="hover:text-foreground">
          {{ owner }} / {{ name }}
        </NuxtLink>
        <span class="mx-1">/</span>
        <span class="font-medium text-foreground">{{ t('repo.settings.title') }}</span>
      </nav>
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('repo.settings.title') }}
      </h1>
    </header>

    <p v-if="loadError" class="text-sm text-destructive">
      {{ loadError }}
    </p>
    <p v-else-if="loading" class="text-sm text-muted-foreground">
      {{ t('common.loading') }}
    </p>

    <template v-else-if="repo && canManage">
      <Card>
        <CardHeader>
          <CardTitle>{{ t('repo.settings.general') }}</CardTitle>
        </CardHeader>
        <Form
          v-slot="{ isSubmitting, values, setFieldValue }"
          :validation-schema="schema"
          :initial-values="initial"
          keep-values
          @submit="onSaveGeneral"
        >
          <CardContent class="space-y-4">
            <FormField v-slot="{ componentField }" name="description">
              <FormItem>
                <FormLabel>{{ t('repo.description') }}</FormLabel>
                <FormControl>
                  <Input type="text" autocomplete="off" v-bind="componentField" />
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField name="visibility">
              <FormItem class="space-y-3">
                <FormLabel>{{ t('repo.visibility') }}</FormLabel>
                <FormControl>
                  <RadioGroup
                    :model-value="values.visibility"
                    class="gap-3"
                    @update:model-value="(v) => setFieldValue('visibility', v as 'public' | 'private')"
                  >
                    <div class="flex items-start gap-3 rounded-md border p-3">
                      <RadioGroupItem id="set-visibility-private" value="private" class="mt-1" />
                      <div class="space-y-0.5">
                        <Label for="set-visibility-private" class="text-sm font-medium">
                          {{ t('repo.visibilityPrivate') }}
                        </Label>
                        <p class="text-xs text-muted-foreground">{{ t('repo.visibilityPrivateHint') }}</p>
                      </div>
                    </div>
                    <div class="flex items-start gap-3 rounded-md border p-3">
                      <RadioGroupItem id="set-visibility-public" value="public" class="mt-1" />
                      <div class="space-y-0.5">
                        <Label for="set-visibility-public" class="text-sm font-medium">
                          {{ t('repo.visibilityPublic') }}
                        </Label>
                        <p class="text-xs text-muted-foreground">{{ t('repo.visibilityPublicHint') }}</p>
                      </div>
                    </div>
                  </RadioGroup>
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField name="default_branch">
              <FormItem>
                <FormLabel>{{ t('repo.defaultBranch') }}</FormLabel>
                <FormControl>
                  <Select
                    :model-value="values.default_branch"
                    @update:model-value="(v) => setFieldValue('default_branch', String(v))"
                  >
                    <SelectTrigger>
                      <SelectValue :placeholder="t('repo.defaultBranch')" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="b in branches" :key="b.name" :value="b.name">
                        {{ b.name }}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <p v-if="generalMsg" :class="generalMsg.kind === 'ok' ? 'text-sm text-emerald-500' : 'text-sm text-destructive'">
              {{ generalMsg.text }}
            </p>
          </CardContent>
          <CardFooter>
            <Button type="submit" :disabled="isSubmitting">
              {{ isSubmitting ? t('repo.settings.saving') : t('repo.settings.saveGeneral') }}
            </Button>
          </CardFooter>
        </Form>
      </Card>

      <Card class="border-destructive/40">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-destructive">
            <AlertTriangle class="size-5" />
            {{ t('repo.settings.danger') }}
          </CardTitle>
        </CardHeader>
        <CardContent class="space-y-6">
          <div class="flex flex-wrap items-start justify-between gap-3 border-b pb-4">
            <div class="space-y-1">
              <p class="text-sm font-medium">{{ t('repo.settings.delete') }}</p>
              <p class="text-xs text-muted-foreground">{{ t('repo.settings.deleteHint') }}</p>
            </div>
            <Button variant="destructive" @click="deleteOpen = true">
              {{ t('repo.settings.delete') }}
            </Button>
          </div>

          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="space-y-1">
              <p class="text-sm font-medium">{{ t('repo.settings.transfer') }}</p>
              <p class="text-xs text-muted-foreground">{{ t('repo.settings.transferHint') }}</p>
            </div>
            <Button variant="outline" disabled>
              {{ t('repo.settings.transferHint') }}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Dialog v-model:open="deleteOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle class="flex items-center gap-2 text-destructive">
              <AlertTriangle class="size-5" />
              {{ t('repo.settings.delete') }}
            </DialogTitle>
            <DialogDescription>
              {{ t('repo.settings.deleteHint') }}
            </DialogDescription>
          </DialogHeader>
          <div class="space-y-3">
            <p class="text-sm">
              {{ t('repo.settings.deleteConfirm', { name: fullName }) }}
            </p>
            <Input v-model="deleteConfirmInput" autocomplete="off" :placeholder="fullName" />
            <p v-if="deleteError" class="text-sm text-destructive">
              {{ deleteError }}
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="deleteOpen = false">
              {{ t('common.cancel') }}
            </Button>
            <Button
              variant="destructive"
              :disabled="deleting || deleteConfirmInput !== fullName"
              @click="onConfirmDelete"
            >
              {{ deleting ? t('repo.settings.deleting') : t('repo.settings.deleteAction') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </template>
  </div>
</template>
