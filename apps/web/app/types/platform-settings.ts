export interface PlatformSetting {
  key: string
  value: string
  default_value: string
  updated_at?: string | null
  updated_by?: string | null
}

export interface PlatformSettingsListResp {
  settings: PlatformSetting[]
}

export interface PlatformSettingPatchReq {
  value: string
}
