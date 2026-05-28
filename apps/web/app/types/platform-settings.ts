export interface PlatformSetting {
  key: string
  value: string
  description: string
  updated_at: string
}

export interface PlatformSettingsListResp {
  items: PlatformSetting[]
}

export interface PlatformSettingPatchReq {
  value: string
}

export interface PlatformSettingResp {
  key: string
  value: string
}

export interface PlatformSettingsBulkPatchReq {
  items: PlatformSettingPatchReq & { key: string }[]
}

export interface PlatformSettingsBulkPatchResp {
  status: string
}

/** Known lifecycle setting keys returned by the server. */
export const LIFECYCLE_KEYS = {
  idleStop: 'lifecycle.idle_stop_threshold',
  idleRemoval: 'lifecycle.idle_removal_threshold',
  abandonedCleanup: 'lifecycle.abandoned_cleanup_threshold',
} as const
