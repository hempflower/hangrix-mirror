package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/hangrix/hangrix/apps/hangrix/internal/agentsconfig"
	"github.com/hangrix/hangrix/apps/hangrix/internal/httpx"
	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/repo/templates"
)

// defaultAgentsYAMLPath is the repo-relative key under which the
// bundled `.hangrix/agents.yml` lives in templates.InitialFiles().
const defaultAgentsYAMLPath = ".hangrix/agents.yml"

// getDefaultAgentsYAML serves the embedded `.hangrix/agents.yml`
// template body. The /repos/new UI uses this to seed its editor so
// the template lives in exactly one place (the embed) instead of
// being duplicated as a TypeScript constant on the web side.
func (h *Handler) getDefaultAgentsYAML(w http.ResponseWriter, r *http.Request) {
	files, err := templates.InitialFiles()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load template: "+err.Error())
		return
	}
	body, ok := files[defaultAgentsYAMLPath]
	if !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "bundled template missing "+defaultAgentsYAMLPath)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// prepareAgentFiles validates a user-pasted `.hangrix/agents.yml`
// body and returns the seed-commit overrides (the yaml itself plus
// stub `.hangrix/prompts/<role>.md` files for any role key whose
// prompt isn't shipped under templates/initial/).
//
// Parse errors short-circuit with a descriptive message so the
// caller can surface a 400 inline — we never let a malformed config
// land in the initial commit, since the runtime would then refuse
// to spawn anything off this repo and the user would have to push a
// fix before they could open their first issue.
func prepareAgentFiles(yamlBody string) (map[string][]byte, error) {
	if strings.TrimSpace(yamlBody) == "" {
		return nil, fmt.Errorf("empty payload")
	}
	body := []byte(yamlBody)
	cfg, err := agentsconfig.ParseHostConfig(body)
	if err != nil {
		return nil, err
	}

	out := map[string][]byte{
		".hangrix/agents.yml": body,
	}

	// Seed a stub prompt file for every role that references one but
	// whose path isn't already part of the bundled template. The four
	// default keys ride on the shipped content; user-added keys would
	// otherwise leave agents.yml pointing at a missing file.
	bundled, err := templates.InitialFiles()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}
	for key, role := range cfg.Roles {
		path := strings.TrimSpace(role.PromptFile)
		if path == "" {
			// Inline `prompt:` — nothing to seed under prompts/.
			continue
		}
		if _, ok := bundled[path]; ok {
			continue
		}
		if _, alreadyOverridden := out[path]; alreadyOverridden {
			continue
		}
		out[path] = []byte(stubPromptBody(key))
	}
	return out, nil
}

// stubPromptBody is the placeholder Markdown shipped as
// `.hangrix/prompts/<role>.md` for a user-added role. The body is
// intentionally bare — the user is expected to flesh it out via
// `git push` once the repo is cloned.
func stubPromptBody(roleKey string) string {
	return fmt.Sprintf("You are the **%s** agent for this repository.\n\nDescribe what this role does, which tools it should call, and any conventions it must follow. Edit this file and `git push` to update.\n", roleKey)
}
