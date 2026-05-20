<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
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
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { PublicOrg } from '~/types/org'

const { t } = useI18n()
const route = useRoute()

setBreadcrumbs(() => {
  const owner = String(route.params.owner ?? '')
  return [
    { label: owner, to: `/${owner}` },
    { label: t('repo.settingsLink') },
  ]
})
useHead({ title: () => `${String(route.params.owner ?? '')} · ${t('org.settings.general')} - ${t('app.name')}` })
const router = useRouter()
const { user, refresh: refreshUser } = useCurrentUser()
const { refresh: refreshMyOrgs } = useMyOrgs()

const orgName = computed(() => String(route.params.owner ?? ''))

const org = ref<PublicOrg | null>(null)
const loading = ref(false)
const loadError = ref<string | null>(null)
const notAnOrg = ref(false)

const displayName = ref('')
const description = ref('')
const avatarURL = ref('')
const generalSaving = ref(false)
const generalMsg = ref<{ kind: 'ok' | 'err'; text: string } | null>(null)

const deleteOpen = ref(false)
const deleteConfirm = ref('')
const deleteError = ref<string | null>(null)
const deleting = ref(false)

async function load() {
  if (!orgName.value) return
  loading.value = true
  loadError.value = null
  notAnOrg.value = false
  try {
    if (!user.value) await refreshUser()
    try {
      org.value = await $fetch<PublicOrg>(`/api/orgs/${orgName.value}`, { credentials: 'include' })
    } catch (e: any) {
      if (e?.response?.status === 404) {
        // Owner exists as a user (or doesn't exist at all) — either way we
        // bounce back to the profile, since users edit their profile under
        // /profile, not /<name>/settings.
        notAnOrg.value = true
        router.replace(`/${orgName.value}`)
        return
      }
      throw e
    }
    if (org.value) {
      displayName.value = org.value.display_name
      description.value = org.value.description
      avatarURL.value = org.value.avatar_url
    }
  } catch (e: any) {
    loadError.value = e?.data?.error ?? t('org.loadFailed')
  } finally {
    loading.value = false
  }
}

async function onSaveGeneral() {
  if (!org.value) return
  generalMsg.value = null
  generalSaving.value = true
  try {
    org.value = await $fetch<PublicOrg>(`/api/orgs/${org.value.name}`, {
      method: 'PATCH',
      credentials: 'include',
      body: {
        display_name: displayName.value,
        description: description.value,
        avatar_url: avatarURL.value,
      },
    })
    generalMsg.value = { kind: 'ok', text: t('org.settings.saved') }
  } catch (e: any) {
    generalMsg.value = { kind: 'err', text: e?.data?.error ?? t('org.settings.saveFailed') }
  } finally {
    generalSaving.value = false
  }
}

async function onDelete() {
  if (!org.value) return
  deleteError.value = null
  if (deleteConfirm.value.trim() !== org.value.name) {
    deleteError.value = t('org.settings.deleteMismatch')
    return
  }
  deleting.value = true
  try {
    await $fetch(`/api/orgs/${org.value.name}`, { method: 'DELETE', credentials: 'include' })
    await refreshMyOrgs()
    router.replace('/')
  } catch (e: any) {
    deleteError.value = e?.data?.error ?? t('org.settings.deleteFailed')
  } finally {
    deleting.value = false
  }
}

watch(orgName, load)
onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ org?.display_name || org?.name || orgName }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('org.settings.subtitle') }}
      </p>
    </header>

    <p v-if="loadError" class="text-sm text-destructive">{{ loadError }}</p>
    <p v-else-if="loading && !org" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <template v-if="org">
      <nav class="flex items-center gap-2 border-b">
        <NuxtLink
          :to="`/${org.name}/settings`"
          :class="['border-b-2 border-primary px-3 py-2 text-sm font-medium']"
        >
          {{ t('org.settings.general') }}
        </NuxtLink>
        <NuxtLink
          :to="`/${org.name}/settings/members`"
          class="border-b-2 border-transparent px-3 py-2 text-sm text-muted-foreground hover:text-foreground"
        >
          {{ t('org.settings.members') }}
        </NuxtLink>
      </nav>

      <Card>
        <CardHeader>
          <CardTitle>{{ t('org.settings.general') }}</CardTitle>
          <CardDescription>{{ t('org.settings.generalHint') }}</CardDescription>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="space-y-1">
            <Label for="org-display-name" class="text-sm">{{ t('org.displayName') }}</Label>
            <Input id="org-display-name" v-model="displayName" autocomplete="off" />
          </div>
          <div class="space-y-1">
            <Label for="org-description" class="text-sm">{{ t('org.description') }}</Label>
            <Input id="org-description" v-model="description" autocomplete="off" />
          </div>
          <div class="space-y-1">
            <Label for="org-avatar" class="text-sm">{{ t('org.avatarURL') }}</Label>
            <Input id="org-avatar" v-model="avatarURL" autocomplete="off" />
          </div>
          <p
            v-if="generalMsg"
            :class="['text-sm', generalMsg.kind === 'ok' ? 'text-primary' : 'text-destructive']"
          >
            {{ generalMsg.text }}
          </p>
        </CardContent>
        <CardFooter>
          <Button :disabled="generalSaving" @click="onSaveGeneral">
            {{ generalSaving ? t('common.saving') : t('common.save') }}
          </Button>
        </CardFooter>
      </Card>

      <Card class="border-destructive/40">
        <CardHeader>
          <CardTitle class="flex items-center gap-2 text-destructive">
            <AlertTriangle class="size-5" />
            {{ t('org.settings.danger') }}
          </CardTitle>
          <CardDescription>{{ t('org.settings.dangerHint') }}</CardDescription>
        </CardHeader>
        <CardContent class="flex flex-wrap items-start justify-between gap-3">
          <div class="space-y-1">
            <p class="text-sm font-medium">{{ t('org.settings.delete') }}</p>
            <p class="text-xs text-muted-foreground">{{ t('org.settings.deleteHint') }}</p>
          </div>
          <Button variant="destructive" @click="deleteOpen = true">{{ t('org.settings.delete') }}</Button>
        </CardContent>
      </Card>

      <Dialog v-model:open="deleteOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle class="flex items-center gap-2 text-destructive">
              <AlertTriangle class="size-5" />
              {{ t('org.settings.delete') }}
            </DialogTitle>
            <DialogDescription>{{ t('org.settings.deleteDescription') }}</DialogDescription>
          </DialogHeader>
          <div class="space-y-3">
            <p class="text-sm">{{ t('org.settings.deleteConfirm', { name: org.name }) }}</p>
            <Input v-model="deleteConfirm" autocomplete="off" :placeholder="org.name" />
            <p v-if="deleteError" class="text-sm text-destructive">{{ deleteError }}</p>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="deleteOpen = false">{{ t('common.cancel') }}</Button>
            <Button
              variant="destructive"
              :disabled="deleteConfirm.trim() !== org.name || deleting"
              @click="onDelete"
            >
              {{ deleting ? t('common.saving') : t('org.settings.delete') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </template>
  </div>
</template>
