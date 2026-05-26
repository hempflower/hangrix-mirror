package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hangrix/hangrix/pkg/actor"

	apidomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/platform_api/domain"
	releasedomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/release/domain"
	runnerdomain "github.com/hangrix/hangrix/apps/hangrix/internal/modules/runner/domain"
)

// releaseCreateTool creates a draft release for the session's repo.
func (r *Registry) releaseCreateTool() *apidomain.Tool {
	return &apidomain.Tool{
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
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
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

			rel, err := r.deps.Releases.Create(ctx, scope.repo.ID, tagName, sha, title, req.Notes, actor.AgentRef(sess.RoleKey))
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

// releaseUploadAssetTool uploads a custom asset to a release.
// The file content is passed as base64 so the platform can persist it
// without needing access to the agent's filesystem.
func (r *Registry) releaseUploadAssetTool() *apidomain.Tool {
	return &apidomain.Tool{
		Name:        "release_upload_asset",
		Description: "Upload a custom asset to a release. The file content must be base64-encoded.",
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
				"content": map[string]any{
					"type":        "string",
					"description": "Base64-encoded file content (required).",
				},
				"content_type": map[string]any{
					"type":        "string",
					"description": "Optional MIME type. Defaults to application/octet-stream.",
				},
			},
			"required": []string{"release_id", "name", "content"},
		},
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
			scope, err := r.loadScope(ctx, sess)
			if err != nil {
				return errorResult(err.Error()), nil
			}
			if r.deps.Releases == nil || r.deps.ReleaseAssets == nil || r.deps.AssetStorage == nil {
				return errorResult("release store not available"), nil
			}
			var req struct {
				ReleaseID   int64  `json:"release_id"`
				Name        string `json:"name"`
				Content     string `json:"content"`
				ContentType string `json:"content_type"`
			}
			if err := unmarshalArgs(args, &req); err != nil {
				return errorResult("invalid arguments: " + err.Error()), nil
			}
			if req.ReleaseID <= 0 {
				return errorResult("release_id is required and must be positive"), nil
			}
			req.Name = strings.TrimSpace(req.Name)
			if req.Name == "" {
				return errorResult("name is required"), nil
			}
			if !isSafeAssetName(req.Name) {
				return errorResult("invalid asset name: must not contain path separators or '..'"), nil
			}

			if req.Content == "" {
				return errorResult("content is required"), nil
			}
			if req.ContentType == "" {
				req.ContentType = "application/octet-stream"
			}

			rel, err := r.deps.Releases.GetByID(ctx, req.ReleaseID)
			if err != nil {
				return errorResult("release not found"), nil
			}
			if rel.RepoID != scope.repo.ID {
				return errorResult("release not in this repo"), nil
			}

			decoded, err := base64.StdEncoding.DecodeString(req.Content)
			if err != nil {
				return errorResult("invalid base64 content: " + err.Error()), nil
			}

			storageKey := fmt.Sprintf("%d/%s", req.ReleaseID, req.Name)
			sizeBytes, err := r.deps.AssetStorage.Store(storageKey, bytes.NewReader(decoded))
			if err != nil {
				return errorResult("store asset: " + err.Error()), nil
			}

			_, err = r.deps.ReleaseAssets.Create(ctx, req.ReleaseID, req.Name, req.ContentType, sizeBytes, storageKey, actor.AgentRef(sess.RoleKey))
			if err != nil {
				_ = r.deps.AssetStorage.Remove(storageKey)
				if errors.Is(err, releasedomain.ErrAssetConflict) {
					return errorResult("an asset with this name already exists on the release"), nil
				}
				return errorResult("create asset: " + err.Error()), nil
			}

			return textResult(map[string]any{
				"ok":           true,
				"release_id":   req.ReleaseID,
				"name":         req.Name,
				"size_bytes":   sizeBytes,
				"content_type": req.ContentType,
			}), nil
		},
	}
}

// releasePublishTool publishes a draft release.
func (r *Registry) releasePublishTool() *apidomain.Tool {
	return &apidomain.Tool{
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
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
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

			pub, err := r.deps.Releases.Publish(ctx, req.ReleaseID, actor.AgentRef(sess.RoleKey))
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
func (r *Registry) releaseUpdateTool() *apidomain.Tool {
	return &apidomain.Tool{
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
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
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
func (r *Registry) releaseDeleteTool() *apidomain.Tool {
	return &apidomain.Tool{
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
		Call: func(ctx context.Context, sess *runnerdomain.AgentSession, args json.RawMessage) (apidomain.Result, error) {
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

			// Clean up asset files from disk and DB records.
			if r.deps.ReleaseAssets != nil {
				assets, _ := r.deps.ReleaseAssets.ListByRelease(ctx, req.ReleaseID)
				for _, a := range assets {
					if r.deps.AssetStorage != nil {
						_ = r.deps.AssetStorage.Remove(a.StorageKey)
					}
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

// isSafeAssetName rejects names that contain path separators, .., or other
// characters that could allow path traversal in AssetStorage paths.
func isSafeAssetName(name string) bool {
	if len(name) > 200 {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	return true
}
