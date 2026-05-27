export interface LifecycleSettings {
  idle_stop_seconds: number
  archive_remove_seconds: number
  periodic_check_seconds: number
}

export interface PlatformSettings {
  lifecycle: LifecycleSettings
}
