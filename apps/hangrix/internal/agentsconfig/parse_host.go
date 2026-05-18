package agentsconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"

)

// hostWire mirrors `.hangrix/agents.yml` on the wire. Anything not
// listed here is rejected by KnownFields(true). The wire type is
// private so domain stays free of yaml struct tags.
type hostWire struct {
	Version   int                       `yaml:"version"`
	Container *containerWire            `yaml:"container"`
	LLM       *llmWire                  `yaml:"llm"`
	Roles     map[string]*roleWire      `yaml:"roles"`
}

type containerWire struct {
	Image   string             `yaml:"image"`
	Build   *buildWire         `yaml:"build"`
	Env     map[string]string  `yaml:"env"`
	Secrets []string           `yaml:"secrets"`
	Volumes []volumeWire       `yaml:"volumes"`
}

type buildWire struct {
	Dockerfile string            `yaml:"dockerfile"`
	Context    string            `yaml:"context"`
	Args       map[string]string `yaml:"args"`
}

type volumeWire struct {
	Name  string `yaml:"name"`
	Mount string `yaml:"mount"`
}

type llmWire struct {
	Model            string  `yaml:"model"`
	MaxOutputTokens  int     `yaml:"max_output_tokens"`
	MaxContextTokens int     `yaml:"max_context_tokens"`
	ReasoningEffort  string  `yaml:"reasoning_effort"`
	Temperature      float64 `yaml:"temperature"`
	TopP             float64 `yaml:"top_p"`
}

type roleWire struct {
	// Triggers is a yaml.Node holding the per-trigger filter mapping.
	// The wire shape rejects unknown filter keys per trigger; doing
	// that with a typed struct requires switching on the trigger name
	// at decode-time, which yaml.v3 doesn't support natively. Keeping
	// the raw node here lets buildRole walk the mapping by hand.
	Triggers   yaml.Node  `yaml:"triggers"`
	Can        []string   `yaml:"can"`
	Not        []string   `yaml:"not"`
	Scope      *scopeWire `yaml:"scope"`
	Prompt     string     `yaml:"prompt"`
	PromptFile string     `yaml:"prompt_file"`
	LLM        *llmWire   `yaml:"llm"`
}

// commentFilterWire is the strict-decode shape for the per-comment
// trigger filter block. Unknown keys (typos like `from_user:` for
// `from_users:`) are rejected by KnownFields(true).
type commentFilterWire struct {
	MentionedOnly bool     `yaml:"mentioned_only"`
	FromRoles     []string `yaml:"from_roles"`
	FromUsers     []string `yaml:"from_users"`
}

// pushFilterWire is the strict-decode shape for the per-push trigger
// filter block.
type pushFilterWire struct {
	Paths       []string `yaml:"paths"`
	PathsIgnore []string `yaml:"paths_ignore"`
}

type scopeWire struct {
	Paths []string `yaml:"paths"`
}

// ParseHostConfig decodes `.hangrix/agents.yml` and validates every
// invariant from docs/agent-config.md. Defaults (e.g. MentionBy ==
// collaborators) are NOT filled here — call NormalizeHostConfig for
// that. Keeping the two passes distinct lets callers tell "user wrote
// 'collaborators' explicitly" apart from "user wrote nothing" if they
// ever need to.
func ParseHostConfig(body []byte) (*HostConfig, error) {
	// Duplicate role-key scan first. yaml.v3 KnownFields(true) also
	// rejects duplicates but does so with a generic "mapping key X
	// already defined" message; promoting the role-key case to its
	// own sentinel lets handlers surface "did you accidentally
	// declare backend twice?" instead of a yaml-internal string.
	if err := rejectDuplicateRoleKeys(body); err != nil {
		return nil, err
	}

	var wire hostWire
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&wire); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf(".hangrix/agents.yml is empty")
		}
		return nil, fmt.Errorf("%w: %s", ErrUnknownField, err.Error())
	}

	if wire.Version != 1 {
		return nil, fmt.Errorf("%w: got %d, want 1", ErrInvalidVersion, wire.Version)
	}

	if wire.Container == nil {
		return nil, fmt.Errorf("%w: container block missing", ErrContainerSourceConflict)
	}
	container, err := buildContainer(wire.Container)
	if err != nil {
		return nil, err
	}

	var teamLLM *LLMConfig
	if wire.LLM != nil {
		llm, err := buildLLM(wire.LLM, "llm")
		if err != nil {
			return nil, err
		}
		teamLLM = llm
	}

	if len(wire.Roles) == 0 {
		return nil, ErrEmptyRoles
	}

	roles := make(map[string]*Role, len(wire.Roles))
	for key, rw := range wire.Roles {
		if !isValidRoleKey(key) {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRoleKey, key)
		}
		if rw == nil {
			return nil, fmt.Errorf("roles.%s: empty role body", key)
		}
		role, err := buildRole(key, rw)
		if err != nil {
			return nil, err
		}
		roles[key] = role
	}

	return &HostConfig{
		Version:   wire.Version,
		Container: container,
		LLM:       teamLLM,
		Roles:     roles,
	}, nil
}

// buildContainer validates and lifts the container block.
//
// image/build is a mutual-exclusive pair: exactly one set. The other
// fields (env, secrets, volumes) are each independently validated.
func buildContainer(w *containerWire) (Container, error) {
	var c Container

	hasImage := w.Image != ""
	hasBuild := w.Build != nil
	if hasImage == hasBuild {
		// Both true OR both false -> conflict.
		return c, fmt.Errorf("%w: image=%t build=%t", ErrContainerSourceConflict, hasImage, hasBuild)
	}
	if hasImage {
		c.Image = w.Image
	}
	if hasBuild {
		if w.Build.Dockerfile == "" {
			return c, fmt.Errorf("%w: build.dockerfile is required", ErrContainerSourceConflict)
		}
		c.Build = &Build{
			Dockerfile: w.Build.Dockerfile,
			Context:    w.Build.Context,
			Args:       w.Build.Args,
		}
	}

	for k := range w.Env {
		if !isValidEnvKey(k) {
			return c, fmt.Errorf("%w: %q", ErrInvalidEnvKey, k)
		}
	}
	c.Env = w.Env

	for _, name := range w.Secrets {
		if !isValidEnvKey(name) {
			return c, fmt.Errorf("%w: %q", ErrInvalidSecretName, name)
		}
	}
	c.Secrets = w.Secrets

	c.Volumes = make([]Volume, 0, len(w.Volumes))
	for i, v := range w.Volumes {
		if v.Name == "" {
			return c, fmt.Errorf("%w: volumes[%d].name empty", ErrInvalidVolumeMount, i)
		}
		if !isValidMountPath(v.Mount) {
			return c, fmt.Errorf("%w: volumes[%d].mount=%q", ErrInvalidVolumeMount, i, v.Mount)
		}
		c.Volumes = append(c.Volumes, Volume{Name: v.Name, Mount: v.Mount})
	}

	return c, nil
}

// buildLLM validates an llm block and lifts it. ctx names the parent
// path ("llm" / "roles.backend.llm") so the error message can pinpoint
// the offending block.
func buildLLM(w *llmWire, ctx string) (*LLMConfig, error) {
	if w.Model == "" {
		return nil, fmt.Errorf("%w: %s.model empty", ErrInvalidModel, ctx)
	}
	if w.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("%w: %s.max_output_tokens=%d (must be >= 0)", ErrInvalidLLMParam, ctx, w.MaxOutputTokens)
	}
	if w.MaxContextTokens < 0 {
		return nil, fmt.Errorf("%w: %s.max_context_tokens=%d (must be >= 0)", ErrInvalidLLMParam, ctx, w.MaxContextTokens)
	}
	if !IsValidReasoningEffort(w.ReasoningEffort) {
		return nil, fmt.Errorf("%w: %s.reasoning_effort=%q (want one of %v)", ErrInvalidLLMParam, ctx, w.ReasoningEffort, ValidReasoningEfforts)
	}
	if w.Temperature < 0 || w.Temperature > 2 {
		return nil, fmt.Errorf("%w: %s.temperature=%v (must be in [0,2])", ErrInvalidLLMParam, ctx, w.Temperature)
	}
	if w.TopP < 0 || w.TopP > 1 {
		return nil, fmt.Errorf("%w: %s.top_p=%v (must be in [0,1])", ErrInvalidLLMParam, ctx, w.TopP)
	}
	return &LLMConfig{
		Model:            w.Model,
		MaxOutputTokens:  w.MaxOutputTokens,
		MaxContextTokens: w.MaxContextTokens,
		ReasoningEffort:  w.ReasoningEffort,
		Temperature:      w.Temperature,
		TopP:             w.TopP,
	}, nil
}

// buildRole validates and lifts one role. The role key is passed in so
// error messages can include it without the caller re-wrapping every
// returned error.
//
// Every role MUST supply exactly one of `prompt:` / `prompt_file:` —
// host yaml is the single source of truth for role prompts (M7c).
func buildRole(key string, w *roleWire) (*Role, error) {
	triggers, err := buildTriggers(key, &w.Triggers)
	if err != nil {
		return nil, err
	}

	if w.Prompt != "" && w.PromptFile != "" {
		return nil, fmt.Errorf("roles.%s: %w", key, ErrPromptMutuallyExclusive)
	}
	if w.PromptFile != "" && !strings.HasPrefix(w.PromptFile, ".hangrix/prompts/") {
		return nil, fmt.Errorf("roles.%s.prompt_file=%q: %w", key, w.PromptFile, ErrInvalidPromptFilePath)
	}
	if w.PromptFile != "" && strings.Contains(w.PromptFile, "..") {
		return nil, fmt.Errorf("roles.%s.prompt_file=%q: %w", key, w.PromptFile, ErrInvalidPromptFilePath)
	}
	if w.Prompt == "" && w.PromptFile == "" {
		return nil, fmt.Errorf("roles.%s: %w", key, ErrPromptMissing)
	}

	var roleLLM *LLMConfig
	if w.LLM != nil {
		llm, err := buildLLM(w.LLM, "roles."+key+".llm")
		if err != nil {
			return nil, err
		}
		roleLLM = llm
	}

	var scope Scope
	if w.Scope != nil {
		scope.Paths = w.Scope.Paths
	}

	for i, n := range w.Not {
		if strings.TrimSpace(n) == "" {
			return nil, fmt.Errorf("roles.%s.not[%d]: empty tool name", key, i)
		}
	}

	return &Role{
		Triggers:   triggers,
		Can:        w.Can,
		Not:        w.Not,
		Scope:      scope,
		Prompt:     w.Prompt,
		PromptFile: w.PromptFile,
		LLM:        roleLLM,
	}, nil
}

// buildTriggers decodes the `triggers:` mapping under one role. The
// wire shape is a yaml map keyed by trigger name; each value is either
// null (no filter, equivalent to `{}`) or an event-specific filter
// block strictly-decoded into the matching wire struct.
//
// Validation rules:
//   - Map must be non-empty (a role with no triggers is a misconfig).
//   - Keys must be recognised Trigger constants.
//   - Filter keys are event-scoped: only `issue.comment` accepts
//     mentioned_only / from_roles / from_users; only `commit.pushed`
//     accepts paths / paths_ignore; the rest must be empty maps.
//   - Duplicate trigger keys (`issue.comment` declared twice) are
//     rejected; yaml.v3's default keeps the last value silently.
func buildTriggers(key string, node *yaml.Node) (map[Trigger]*TriggerSpec, error) {
	// `triggers:` omitted entirely → node is the zero value
	// (Kind == 0). Treat the same as an empty map: missing required.
	if node == nil || node.Kind == 0 {
		return nil, fmt.Errorf("roles.%s.triggers: %w", key, ErrEmptyTriggers)
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("roles.%s.triggers: %w (must be a mapping of event-name → filter)", key, ErrInvalidTriggerSpec)
	}
	if len(node.Content) == 0 {
		return nil, fmt.Errorf("roles.%s.triggers: %w", key, ErrEmptyTriggers)
	}

	out := make(map[Trigger]*TriggerSpec, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		kn := node.Content[i]
		vn := node.Content[i+1]
		name := kn.Value
		if !IsValidTrigger(name) {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w", key, name, ErrUnknownTrigger)
		}
		t := Trigger(name)
		if _, dup := out[t]; dup {
			return nil, fmt.Errorf("roles.%s.triggers.%s: duplicate trigger key", key, name)
		}
		spec, err := buildTriggerSpec(key, t, vn)
		if err != nil {
			return nil, err
		}
		out[t] = spec
	}
	return out, nil
}

// buildTriggerSpec validates the per-event filter block for one
// trigger entry. A null / empty mapping yields the zero TriggerSpec.
// Otherwise the parser strict-decodes into the appropriate filter wire
// (rejecting unknown keys) and dispatches based on the event name.
func buildTriggerSpec(roleKey string, t Trigger, value *yaml.Node) (*TriggerSpec, error) {
	// Empty block: `event-name:` (null scalar) or `event-name: {}`
	// (empty mapping) both decode to "no filters". Treat them
	// identically — a yaml author can use whichever reads better.
	emptyBlock := value == nil || value.Kind == 0 ||
		(value.Kind == yaml.ScalarNode && value.Tag == "!!null") ||
		(value.Kind == yaml.MappingNode && len(value.Content) == 0)

	switch t {
	case TriggerIssueComment:
		if emptyBlock {
			return &TriggerSpec{Comment: &CommentFilter{}}, nil
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w (filter must be a mapping)", roleKey, t, ErrInvalidTriggerSpec)
		}
		var cw commentFilterWire
		if err := decodeKnown(value, &cw); err != nil {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w: %s", roleKey, t, ErrInvalidTriggerSpec, err.Error())
		}
		return &TriggerSpec{Comment: &CommentFilter{
			MentionedOnly: cw.MentionedOnly,
			FromRoles:     cw.FromRoles,
			FromUsers:     cw.FromUsers,
		}}, nil

	case TriggerCommitPushed:
		if emptyBlock {
			return &TriggerSpec{Push: &PushFilter{}}, nil
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w (filter must be a mapping)", roleKey, t, ErrInvalidTriggerSpec)
		}
		var pw pushFilterWire
		if err := decodeKnown(value, &pw); err != nil {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w: %s", roleKey, t, ErrInvalidTriggerSpec, err.Error())
		}
		return &TriggerSpec{Push: &PushFilter{
			Paths:       pw.Paths,
			PathsIgnore: pw.PathsIgnore,
		}}, nil

	default:
		// Filter-less triggers: issue.opened / issue.closed /
		// review_vote.posted / ci.status_changed. They accept only an
		// empty body; any key is a misconfiguration.
		if !emptyBlock {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w (event takes no filters)", roleKey, t, ErrInvalidTriggerSpec)
		}
		return &TriggerSpec{}, nil
	}
}

// decodeKnown re-encodes a yaml.Node and re-decodes it with
// KnownFields(true) so unknown keys inside the trigger filter block
// surface as errors. yaml.v3's Node.Decode does not honour
// KnownFields on the outer decoder, so we round-trip through Marshal.
func decodeKnown(n *yaml.Node, into any) error {
	buf, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(buf))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%w: %s", ErrUnknownField, err.Error())
	}
	return nil
}

// isValidRoleKey matches `^[a-z][a-z0-9-]{0,38}$`. Same shape as the
// mention-protocol grammar (`@agent-<role-key>`) so role keys can be
// embedded into mentions and commit-author strings without escaping.
// Length cap of 39 is paranoid-room for the `agent-` prefix plus a
// git-friendly identifier.
func isValidRoleKey(s string) bool {
	if s == "" || len(s) > 39 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isValidEnvKey matches `^[A-Z_][A-Z0-9_]*$`. Uppercase-only matches
// POSIX convention; `_` may lead so `_LEGACY` style is OK.
func isValidEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// isValidMountPath requires an absolute path with no `..` segments. We
// don't path.Clean it — the user-facing error should refer to the
// literal string from yaml so the operator can find it in their editor.
func isValidMountPath(s string) bool {
	if !strings.HasPrefix(s, "/") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// rejectDuplicateRoleKeys walks the raw yaml tree for the `roles:`
// mapping and flags any key that appears more than once. yaml.v3's
// default behaviour is to silently keep the last entry, which would
// let a config drift undetected after a copy-paste.
func rejectDuplicateRoleKeys(body []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return nil // strict decode will fail with the canonical msg
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "roles" {
			continue
		}
		rolesNode := top.Content[i+1]
		if rolesNode.Kind != yaml.MappingNode {
			return nil
		}
		seen := make(map[string]struct{}, len(rolesNode.Content)/2)
		for j := 0; j+1 < len(rolesNode.Content); j += 2 {
			k := rolesNode.Content[j].Value
			if _, dup := seen[k]; dup {
				return fmt.Errorf("%w: %q", ErrDuplicateRoleKey, k)
			}
			seen[k] = struct{}{}
		}
	}
	return nil
}
