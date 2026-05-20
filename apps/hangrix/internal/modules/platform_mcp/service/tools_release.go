package service

import (
	"context"
	"encoding/json"
	"strings"

	platformmcpdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_mcp/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// releaseCreateTool creates a draft release for the session's repo.
func (r *Registry) releaseCreateTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "release_create",
		Description: "Create a draft release. The tag must already exist in the repository. Returns the release with id, tag_name, and draft status.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tag_name": map[string]any{
					"type":        "string",
					"description": "The existing git tag to create the release for (required).",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Release title. Defaults to the tag name if omitted.",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "Release notes / description (markdown).",
				},
			},
			"required": []string{"tag_name"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if r.deps.Releases == nil {
				return errorResult("release store not available"), nil
			}
			var req struct {
				TagName string `json:"tag_name"`
				Title   string `json:"title"`
				Notes   string `json:"notes"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			tagName := strings.TrimSpace(req.TagName)
			if tagName == "" {
				return errorResult("tag_name is required"), nil
			}
			title := strings.TrimSpace(req.Title)
			if title == "" {
				title = tagName
			}

			sha, err := r.deps.Git.ResolveCommit(scope.fsPath, "refs/tags/"+tagName)
			if err != nil || sha == "" {
				return errorResult("tag not found: " + tagName), nil
			}

			rel, err := r.deps.Releases.Create(ctx, scope.repo.ID, tagName, sha, title, req.Notes)
			if err != nil {
				return errorResult("create release: " + err.Error()), nil
			}
			return textResult(map[string]any{
				"id":                rel.ID,
				"tag_name":          rel.TagName,
				"target_commit_sha": rel.TargetCommitSHA,
				"title":             rel.Title,
				"is_draft":          rel.IsDraft,
				"created_at":        stableTime(rel.CreatedAt),
			}), nil
		},
	}
}

// releaseUploadAssetTool is a descriptor-only tool. The actual upload
// goes through the agent runtime's HTTP multipart endpoint.
func (r *Registry) releaseUploadAssetTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "release_upload_asset",
		Description: "Upload a custom asset to a release. The asset binary is read from a local file path on the agent's filesystem and uploaded via HTTP multipart.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"release_id": map[string]any{
					"type":        "integer",
					"description": "The release ID to attach the asset to (required).",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Asset file name (required).",
				},
				"file_path": map[string]any{
					"type":        "string",
					"description": "Local path to the file to upload (required).",
				},
			},
			"required": []string{"release_id", "name", "file_path"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			return errorResult("release_upload_asset: use the platform HTTP endpoint — POST /api/repos/{owner}/{name}/releases/{id}/assets with multipart form"), nil
		},
	}
}

// releasePublishTool publishes a draft release.
func (r *Registry) releasePublishTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "release_publish",
		Description: "Publish a draft release, making it visible as an official release.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"release_id": map[string]any{
					"type":        "integer",
					"description": "The release ID to publish (required).",
				},
			},
			"required": []string{"release_id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if r.deps.Releases == nil {
				return errorResult("release store not available"), nil
			}
			var req struct {
				ReleaseID int64 `json:"release_id"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ReleaseID <= 0 {
				return errorResult("release_id is required and must be positive"), nil
			}

			rel, err := r.deps.Releases.GetByID(ctx, req.ReleaseID)
			if err != nil {
				return errorResult("release not found"), nil
			}
			if rel.RepoID != scope.repo.ID {
				return errorResult("release not in this repo"), nil
			}

			pub, err := r.deps.Releases.Publish(ctx, req.ReleaseID)
			if err != nil {
				return errorResult("publish: " + err.Error()), nil
			}
			return textResult(map[string]any{
				"id":           pub.ID,
				"tag_name":     pub.TagName,
				"is_draft":     pub.IsDraft,
				"published_at": stableTime(pub.PublishedAt),
			}), nil
		},
	}
}

// releaseUpdateTool updates a release's title, notes, or tag_name.
func (r *Registry) releaseUpdateTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "release_update",
		Description: "Update a release's title, notes, or tag_name. Only draft releases can change tag_name.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"release_id": map[string]any{
					"type":        "integer",
					"description": "The release ID to update (required).",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "New title.",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "New release notes (markdown).",
				},
				"tag_name": map[string]any{
					"type":        "string",
					"description": "New tag name (draft only).",
				},
			},
			"required": []string{"release_id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if r.deps.Releases == nil {
				return errorResult("release store not available"), nil
			}
			var req struct {
				ReleaseID int64  `json:"release_id"`
				Title     string `json:"title"`
				Notes     string `json:"notes"`
				TagName   string `json:"tag_name"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ReleaseID <= 0 {
				return errorResult("release_id is required and must be positive"), nil
			}

			rel, err := r.deps.Releases.GetByID(ctx, req.ReleaseID)
			if err != nil {
				return errorResult("release not found"), nil
			}
			if rel.RepoID != scope.repo.ID {
				return errorResult("release not in this repo"), nil
			}

			tagName := rel.TagName
			targetSHA := rel.TargetCommitSHA
			if req.TagName != "" && req.TagName != rel.TagName {
				if !rel.IsDraft {
					return errorResult("cannot change tag_name of a published release"), nil
				}
				sha, err := r.deps.Git.ResolveCommit(scope.fsPath, "refs/tags/"+req.TagName)
				if err != nil || sha == "" {
					return errorResult("tag not found: " + req.TagName), nil
				}
				tagName = req.TagName
				targetSHA = sha
			}

			title := rel.Title
			if req.Title != "" {
				title = req.Title
			}
			notes := rel.Notes
			if req.Notes != "" {
				notes = req.Notes
			}

			updated, err := r.deps.Releases.Update(ctx, req.ReleaseID, tagName, targetSHA, title, notes)
			if err != nil {
				return errorResult("update release: " + err.Error()), nil
			}
			return textResult(map[string]any{
				"id":                updated.ID,
				"tag_name":          updated.TagName,
				"target_commit_sha": updated.TargetCommitSHA,
				"title":             updated.Title,
				"is_draft":          updated.IsDraft,
				"updated_at":        stableTime(updated.UpdatedAt),
			}), nil
		},
	}
}

// releaseDeleteTool deletes a release and its assets.
func (r *Registry) releaseDeleteTool() *platformmcpdomain.Tool {
	return &platformmcpdomain.Tool{
		Name:        "release_delete",
		Description: "Delete a release and all its custom assets.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"release_id": map[string]any{
					"type":        "integer",
					"description": "The release ID to delete (required).",
				},
			},
			"required": []string{"release_id"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (platformmcpdomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if r.deps.Releases == nil {
				return errorResult("release store not available"), nil
			}
			var req struct {
				ReleaseID int64 `json:"release_id"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ReleaseID <= 0 {
				return errorResult("release_id is required and must be positive"), nil
			}

			rel, err := r.deps.Releases.GetByID(ctx, req.ReleaseID)
			if err != nil {
				return errorResult("release not found"), nil
			}
			if rel.RepoID != scope.repo.ID {
				return errorResult("release not in this repo"), nil
			}

			// Clean up assets from DB if asset store is available.
			if r.deps.ReleaseAssets != nil {
				assets, _ := r.deps.ReleaseAssets.ListByRelease(ctx, req.ReleaseID)
				for _, a := range assets {
					_ = r.deps.ReleaseAssets.Delete(ctx, a.ID)
				}
			}

			if err := r.deps.Releases.Delete(ctx, req.ReleaseID); err != nil {
				return errorResult("delete release: " + err.Error()), nil
			}
			return textResult(map[string]any{
				"deleted": true,
			}), nil
		},
	}
}
