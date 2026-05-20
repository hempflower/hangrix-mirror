// Release data model matching the server-side REST API
// /api/repos/{owner}/{name}/releases

export interface ReleaseAsset {
  id: number
  release_id: number
  name: string
  content_type: string
  size_bytes: number
  created_at: string
}

// Source archives are derived, not stored — returned as download URLs.
export interface SourceArchive {
  format: 'zip' | 'tar.gz'
  url: string
}

export interface Release {
  id: number
  repo_id: number
  tag_name: string
  /** Commit SHA the tag resolved to at creation time. */
  target_commit_sha: string
  title: string
  notes: string
  is_draft: boolean
  published_at: string | null
  created_at: string
  updated_at: string
  /** Derived source archives (available for every release). */
  source_archives?: SourceArchive[]
  /** Custom uploaded assets. */
  assets?: ReleaseAsset[]
}

export interface ReleaseListResp {
  items: Release[]
  total: number
}

export interface ReleaseCreateReq {
  tag_name: string
  title?: string
  notes?: string
}

export interface ReleaseUpdateReq {
  title?: string
  notes?: string
  /** Only allowed while still a draft. */
  tag_name?: string
}
