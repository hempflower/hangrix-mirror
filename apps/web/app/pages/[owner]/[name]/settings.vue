<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { AlertTriangle, Clock, Plus, Settings, Shield, Trash2 } from 'lucide-vue-next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import type { BranchProtection, PublicRepo, RepoRefs } from '~/types/repo'
import AutomationSettings from '@/components/repo/AutomationSettings.vue'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  const name = String(route.params.name ?? '')
  const base = `/${owner}/${name}`
  return [
    { label: owner, to: base },
    { label: name, to: base },
    { label: t('repo.settingsLink') },
  ]
})
const router = useRouter()
const { user, refresh: refreshUser } = useCurrentUser()
const { orgs: myOrgs, refresh: refreshMyOrgs } = useMyOrgs()

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

const protections = ref<BranchProtection[]>([])
const protectionError = ref<string | null>(null)
const protectionOpen = ref(false)
const protectionForm = ref({
  pattern: '',
  forbid_force_push: true,
  forbid_delete: true,
  forbid_direct_push: false,
})
const protectionSubmitError = ref<string | null>(null)
const protectionSubmitting = ref(false)

const branches = computed(() => refs.value?.branches ?? [])
const fullName = computed(() => `${owner.value}/${name.value}`)
const canManage = computed(() => {
  if (!repo.value || !user.value) return false
  if (user.value.role === 'admin') return true
  if (repo.value.owner_kind === 'user') {
    return user.value.id === repo.value.owner_id
  }
  // Org-owned: optimistic UI check — show the controls if the repo is
  // owned by an org the caller belongs to. The server still enforces
  // owner-role on every mutation, so non-owner members hitting the
  // buttons just get a 403.
  return (myOrgs.value ?? []).some(o => o.id === repo.value!.owner_id)
})

const transferOpen = ref(false)
const transferTarget = ref('')
const transferConfirm = ref('')
const transferError = ref<string | null>(null)
const transferring = ref(false)

async function onTransfer() {
  if (!repo.value) return
  transferError.value = null
  transferring.value = true
  try {
    const updated = await $fetch<PublicRepo>(
      `/api/repos/${owner.value}/${name.value}/transfer`,
      {
        method: 'POST',
        credentials: 'include',
        body: { target_owner: transferTarget.value.trim(), confirm: transferConfirm.value.trim() },
      },
    )
    transferOpen.value = false
    transferTarget.value = ''
    transferConfirm.value = ''
    router.replace(`/${updated.owner_name}/${updated.name}/settings`)
  } catch (e: any) {
    transferError.value = e?.data?.error ?? t('repo.settings.transferFailed')
  } finally {
    transferring.value = false
  }
}

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
    if (!myOrgs.value) await refreshMyOrgs()
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
    await loadProtections()
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('repo.notFound')
  } finally {
    loading.value = false
  }
}

async function loadProtections() {
  protectionError.value = null
  try {
    const data = await $fetch<BranchProtection[]>(
      `/api/repos/${owner.value}/${name.value}/branch-protections`,
      { credentials: 'include' },
    )
    protections.value = data ?? []
  } catch (e: any) {
    protectionError.value = e?.data?.error ?? t('repo.protections.loadFailed')
    protections.value = []
  }
}

function resetProtectionForm() {
  protectionForm.value = {
    pattern: '',
    forbid_force_push: true,
    forbid_delete: true,
    forbid_direct_push: false,
  }
  protectionSubmitError.value = null
}

function openProtectionDialog() {
  resetProtectionForm()
  protectionOpen.value = true
}

async function onCreateProtection() {
  if (!protectionForm.value.pattern.trim()) {
    protectionSubmitError.value = t('repo.protections.patternRequired')
    return
  }
  protectionSubmitting.value = true
  protectionSubmitError.value = null
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/branch-protections`, {
      method: 'POST',
      credentials: 'include',
      body: {
        pattern: protectionForm.value.pattern.trim(),
        forbid_force_push: protectionForm.value.forbid_force_push,
        forbid_delete: protectionForm.value.forbid_delete,
        forbid_direct_push: protectionForm.value.forbid_direct_push,
      },
    })
    protectionOpen.value = false
    resetProtectionForm()
    await loadProtections()
  } catch (e: any) {
    protectionSubmitError.value = e?.data?.error ?? t('repo.protections.saveFailed')
  } finally {
    protectionSubmitting.value = false
  }
}

async function onToggleProtection(rule: BranchProtection, field: 'forbid_force_push' | 'forbid_delete' | 'forbid_direct_push', next: boolean) {
  try {
    const updated = await $fetch<BranchProtection>(
      `/api/repos/${owner.value}/${name.value}/branch-protections/${rule.id}`,
      {
        method: 'PATCH',
        credentials: 'include',
        body: {
          pattern: rule.pattern,
          forbid_force_push: field === 'forbid_force_push' ? next : rule.forbid_force_push,
          forbid_delete: field === 'forbid_delete' ? next : rule.forbid_delete,
          forbid_direct_push: field === 'forbid_direct_push' ? next : rule.forbid_direct_push,
        },
      },
    )
    const idx = protections.value.findIndex(p => p.id === rule.id)
    if (idx >= 0) protections.value[idx] = updated
  } catch (e: any) {
    protectionError.value = e?.data?.error ?? t('repo.protections.saveFailed')
  }
}

async function onDeleteProtection(rule: BranchProtection) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('repo.protections.deleteConfirm', { pattern: rule.pattern }))) return
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/branch-protections/${rule.id}`, {
      method: 'DELETE',
      credentials: 'include',
    })
    await loadProtections()
  } catch (e: any) {
    protectionError.value = e?.data?.error ?? t('repo.protections.deleteFailed')
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

    <Tabs v-else-if="repo && canManage" default-value="general" class="space-y-6">
      <TabsList>
        <TabsTrigger value="general">
          <Settings class="size-4" />
          <span class="ml-1.5">{{ t('repo.settings.general') }}</span>
        </TabsTrigger>
        <TabsTrigger value="automation">
          <Clock class="size-4" />
          <span class="ml-1.5">{{ t('repo.automation.tabLabel') }}</span>
        </TabsTrigger>
      </TabsList>

      <TabsContent value="general" class="space-y-6 mt-0">
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

      <Card>
        <CardHeader>
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="space-y-1">
              <CardTitle class="flex items-center gap-2">
                <Shield class="size-5" />
                {{ t('repo.protections.title') }}
              </CardTitle>
              <CardDescription>{{ t('repo.protections.subtitle') }}</CardDescription>
            </div>
            <Button size="sm" @click="openProtectionDialog">
              <Plus class="size-4" />
              {{ t('repo.protections.create') }}
            </Button>
          </div>
        </CardHeader>
        <CardContent class="space-y-3">
          <p v-if="protectionError" class="text-sm text-destructive">
            {{ protectionError }}
          </p>
          <p v-if="protections.length === 0" class="text-sm text-muted-foreground">
            {{ t('repo.protections.empty') }}
          </p>
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t('repo.protections.pattern') }}</TableHead>
                <TableHead class="text-center">{{ t('repo.protections.forbidForcePush') }}</TableHead>
                <TableHead class="text-center">{{ t('repo.protections.forbidDelete') }}</TableHead>
                <TableHead class="text-center">{{ t('repo.protections.forbidDirectPush') }}</TableHead>
                <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="rule in protections" :key="rule.id">
                <TableCell class="font-mono text-sm">
                  {{ rule.pattern }}
                </TableCell>
                <TableCell class="text-center">
                  <Checkbox
                    :model-value="rule.forbid_force_push"
                    @update:model-value="(v) => onToggleProtection(rule, 'forbid_force_push', Boolean(v))"
                  />
                </TableCell>
                <TableCell class="text-center">
                  <Checkbox
                    :model-value="rule.forbid_delete"
                    @update:model-value="(v) => onToggleProtection(rule, 'forbid_delete', Boolean(v))"
                  />
                </TableCell>
                <TableCell class="text-center">
                  <div class="inline-flex items-center gap-1">
                    <Checkbox
                      :model-value="rule.forbid_direct_push"
                      @update:model-value="(v) => onToggleProtection(rule, 'forbid_direct_push', Boolean(v))"
                    />

                  </div>
                </TableCell>
                <TableCell class="text-right">
                  <Button size="sm" variant="outline" @click="onDeleteProtection(rule)">
                    <Trash2 class="size-3" />
                  </Button>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
          <p class="text-xs text-muted-foreground">
            {{ t('repo.protections.hint') }}
          </p>
        </CardContent>
      </Card>

      <Dialog v-model:open="protectionOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{{ t('repo.protections.create') }}</DialogTitle>
            <DialogDescription>{{ t('repo.protections.subtitle') }}</DialogDescription>
          </DialogHeader>
          <div class="space-y-4">
            <div class="space-y-2">
              <Label for="protection-pattern">{{ t('repo.protections.pattern') }}</Label>
              <Input
                id="protection-pattern"
                v-model="protectionForm.pattern"
                autocomplete="off"
                placeholder="main"
              />
              <p class="text-xs text-muted-foreground">
                {{ t('repo.protections.patternHint') }}
              </p>
            </div>
            <div class="space-y-2">
              <Label class="flex items-center gap-2">
                <Checkbox v-model="protectionForm.forbid_force_push" />
                <span>{{ t('repo.protections.forbidForcePush') }}</span>
              </Label>
              <Label class="flex items-center gap-2">
                <Checkbox v-model="protectionForm.forbid_delete" />
                <span>{{ t('repo.protections.forbidDelete') }}</span>
              </Label>
              <Label class="flex items-center gap-2">
                <Checkbox v-model="protectionForm.forbid_direct_push" />
                <span class="flex items-center gap-2">
                  {{ t('repo.protections.forbidDirectPush') }}

                </span>
              </Label>
            </div>
            <p v-if="protectionSubmitError" class="text-sm text-destructive">
              {{ protectionSubmitError }}
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="protectionOpen = false">
              {{ t('common.cancel') }}
            </Button>
            <Button :disabled="protectionSubmitting" @click="onCreateProtection">
              {{ protectionSubmitting ? t('repo.protections.submitting') : t('repo.protections.submit') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
            <Button variant="outline" @click="transferOpen = true">
              {{ t('repo.settings.transfer') }}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Dialog v-model:open="transferOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{{ t('repo.settings.transfer') }}</DialogTitle>
            <DialogDescription>
              {{ t('repo.settings.transferDescription') }}
            </DialogDescription>
          </DialogHeader>
          <div class="space-y-3">
            <div class="space-y-1">
              <Label for="transfer-target" class="text-sm">
                {{ t('repo.settings.transferTarget') }}
              </Label>
              <Input id="transfer-target" v-model="transferTarget" autocomplete="off" :placeholder="t('repo.settings.transferTargetPlaceholder')" />
            </div>
            <div class="space-y-1">
              <Label for="transfer-confirm" class="text-sm">
                {{ t('repo.settings.transferConfirmLabel', { name: fullName }) }}
              </Label>
              <Input id="transfer-confirm" v-model="transferConfirm" autocomplete="off" :placeholder="fullName" />
            </div>
            <p v-if="transferError" class="text-sm text-destructive">
              {{ transferError }}
            </p>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="transferOpen = false">
              {{ t('common.cancel') }}
            </Button>
            <Button
              :disabled="!transferTarget.trim() || transferConfirm.trim() !== fullName || transferring"
              @click="onTransfer"
            >
              {{ transferring ? t('common.saving') : t('repo.settings.transfer') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

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
      </TabsContent>

      <TabsContent value="automation" class="mt-0">
        <AutomationSettings :owner="owner" :name="name" />
      </TabsContent>
    </Tabs>
  </div>
</template>
