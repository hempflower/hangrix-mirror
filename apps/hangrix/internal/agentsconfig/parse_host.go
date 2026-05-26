package agentsconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// hostWire mirrors `.hangrix/agents.yml` on the wire. Unknown keys are
// silently dropped (forward-compatibility: hosts MAY ship newer fields
// against an older agent server without bricking the parser). The wire
// type is private so domain stays free of yaml struct tags.
type hostWire struct {
	Version   int                 `yaml:"version"`
	Container *containerWire      `yaml:"container"`
	LLM       *llmWire            `yaml:"llm"`
	Issues    *issuesWire         `yaml:"issues"`
	Reviewers *reviewersWire      `yaml:"reviewers"`
	Tools     map[string][]string `yaml:"tools"`
}

type issuesWire struct {
	DeleteBranchOnMerge *bool `yaml:"delete_branch_on_merge"`
}

type reviewersWire struct {
	Rules    []reviewerRuleWire `yaml:"rules"`
	Fallback []string           `yaml:"fallback"`
}

type reviewerRuleWire struct {
	Paths     []string `yaml:"paths"`
	Reviewers []string `yaml:"reviewers"`
}

type containerWire struct {
	Image      string            `yaml:"image"`
	Build      *buildWire        `yaml:"build"`
	Entrypoint []string          `yaml:"entrypoint"`
	Env        map[string]string `yaml:"env"`
	Volumes    []volumeWire      `yaml:"volumes"`
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

// llmWire mirrors the yaml `llm:` block. Every tuning field is a
// pointer so the parser can tell "field omitted" from "field set to
// zero" — required to support per-field inheritance from team default
// to role override (see LLMConfig docstring).
type llmWire struct {
	Model            string   `yaml:"model"`
	MaxOutputTokens  *int     `yaml:"max_output_tokens"`
	MaxContextTokens *int     `yaml:"max_context_tokens"`
	ReasoningEffort  *string  `yaml:"reasoning_effort"`
	Temperature      *float64 `yaml:"temperature"`
	TopP             *float64 `yaml:"top_p"`
}

// roleWire mirrors the YAML front matter of a `.hangrix/agents/<role>.md`
// file. The role's prompt is the Markdown body after the front matter,
// not a wire field. Unknown keys are dropped (forward-compat).
type roleWire struct {
	// Triggers is a yaml.Node holding the per-trigger filter mapping.
	// The wire shape rejects unknown filter keys per trigger; doing
	// that with a typed struct requires switching on the trigger name
	// at decode-time, which yaml.v3 doesn't support natively. Keeping
	// the raw node here lets buildRole walk the mapping by hand.
	Triggers   yaml.Node  `yaml:"triggers"`
	Permission string     `yaml:"permission"`
	Tools      []string   `yaml:"tools"`
	Scope      *scopeWire `yaml:"scope"`
	LLM        *llmWire   `yaml:"llm"`
	Mcp        []string   `yaml:"mcp"`
}

// commentFilterWire is the per-comment trigger filter block. Unknown
// keys are silently dropped; the parser only validates the keys it
// recognises. A typo like `from_user:` instead of `from_users:` will
// therefore leave the filter empty rather than fail at parse time —
// authors SHOULD diff the canonical key names if a trigger fires more
// or less often than expected.
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
	var wire hostWire
	dec := yaml.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&wire); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf(".hangrix/agents.yml is empty")
		}
		return nil, fmt.Errorf("decode .hangrix/agents.yml: %s", err.Error())
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
		llm, err := buildLLM(wire.LLM, "llm", true)
		if err != nil {
			return nil, err
		}
		teamLLM = llm
	}

	issues := buildIssues(wire.Issues)

	tools, err := buildToolRules(wire.Tools)
	if err != nil {
		return nil, err
	}

	// Reviewers structure is validated here; reviewer-role existence and
	// vote-capability are checked later in AssembleHostConfig, once the
	// per-role files have been loaded.
	reviewers, err := buildReviewers(wire.Reviewers)
	if err != nil {
		return nil, err
	}

	return &HostConfig{
		Version:   wire.Version,
		Container: container,
		LLM:       teamLLM,
		Issues:    issues,
		Reviewers: reviewers,
		Tools:     tools,
		// Roles are populated by LoadHostConfig from .hangrix/agents/*.md.
	}, nil
}

// buildToolRules validates and lifts the `tools:` rule map. Each rule is
// a named whitelist of platform tool-name glob patterns. Rule names must
// be non-empty; patterns must be non-empty (a `*` wildcard is allowed).
// nil map is fine (no rules).
func buildToolRules(w map[string][]string) (map[string][]string, error) {
	if len(w) == 0 {
		return nil, nil
	}
	out := make(map[string][]string, len(w))
	for name, patterns := range w {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%w: empty rule name", ErrInvalidToolRule)
		}
		ps := make([]string, 0, len(patterns))
		for i, p := range patterns {
			if strings.TrimSpace(p) == "" {
				return nil, fmt.Errorf("%w: tools.%s[%d] empty pattern", ErrInvalidToolRule, name, i)
			}
			ps = append(ps, p)
		}
		out[name] = ps
	}
	return out, nil
}

// buildReviewers validates and lifts the structure of the optional
// `reviewers:` block. An absent block yields nil (no review gate). Every
// rule must declare both paths and reviewers, and `fallback:` must be
// non-empty (so a contribution can never be left with no possible
// reviewer). Reviewer-ROLE existence and vote-capability are NOT checked
// here — that happens in validateReviewers during AssembleHostConfig,
// after the per-role files are loaded.
func buildReviewers(w *reviewersWire) (*ReviewersConfig, error) {
	if w == nil {
		return nil, nil
	}
	cfg := &ReviewersConfig{}
	for i, rw := range w.Rules {
		field := fmt.Sprintf("reviewers.rules[%d]", i)
		if len(rw.Paths) == 0 {
			return nil, fmt.Errorf("%w: %s.paths is required", ErrInvalidReviewers, field)
		}
		if len(rw.Reviewers) == 0 {
			return nil, fmt.Errorf("%w: %s.reviewers is required", ErrInvalidReviewers, field)
		}
		for _, p := range rw.Paths {
			if strings.TrimSpace(p) == "" {
				return nil, fmt.Errorf("%w: %s.paths contains an empty pattern", ErrInvalidReviewers, field)
			}
		}
		cfg.Rules = append(cfg.Rules, ReviewerRule{
			Paths:     append([]string(nil), rw.Paths...),
			Reviewers: append([]string(nil), rw.Reviewers...),
		})
	}
	if len(w.Fallback) == 0 {
		return nil, fmt.Errorf("%w: reviewers.fallback is required (used when no rule matches)", ErrInvalidReviewers)
	}
	cfg.Fallback = append([]string(nil), w.Fallback...)
	return cfg, nil
}

// validateReviewers checks that every reviewer role referenced by the
// `reviewers:` block exists and can cast review votes. Called from
// AssembleHostConfig once roles (and their resolved ToolPatterns) exist.
func validateReviewers(cfg *ReviewersConfig, roles map[string]*Role) error {
	if cfg == nil {
		return nil
	}
	check := func(field, key string) error {
		role, ok := roles[key]
		if !ok {
			return fmt.Errorf("%w: %s references unknown role %q", ErrInvalidReviewers, field, key)
		}
		if !roleCanVote(role) {
			return fmt.Errorf("%w: %s reviewer %q cannot cast votes (needs permission: write and issue_review_vote in its tool rules)", ErrInvalidReviewers, field, key)
		}
		return nil
	}
	for i, r := range cfg.Rules {
		field := fmt.Sprintf("reviewers.rules[%d].reviewers", i)
		for _, rv := range r.Reviewers {
			if err := check(field, rv); err != nil {
				return err
			}
		}
	}
	for _, rv := range cfg.Fallback {
		if err := check("reviewers.fallback", rv); err != nil {
			return err
		}
	}
	return nil
}

// roleCanVote reports whether a role is permitted to call issue_review_vote.
// Voting is a write operation, so the role needs "write" permission, and
// issue_review_vote must be permitted by the role's resolved tool
// whitelist (ToolPatterns).
func roleCanVote(role *Role) bool {
	if role.Permission != "write" {
		return false
	}
	return matchesAnyGlob(role.ToolPatterns, "issue_review_vote")
}

func containsStringFold(list []string, want string) bool {
	for _, s := range list {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

// buildContainer validates and lifts the container block.
//
// image/build is a mutual-exclusive pair: exactly one set. The other
// fields (env, volumes) are each independently validated.
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

	for i, arg := range w.Entrypoint {
		if arg == "" {
			return c, fmt.Errorf("%w: entrypoint[%d]: empty string", ErrInvalidContainerEntrypoint, i)
		}
	}
	c.Entrypoint = append([]string(nil), w.Entrypoint...)

	for k := range w.Env {
		if !isValidEnvKey(k) {
			return c, fmt.Errorf("%w: %q", ErrInvalidEnvKey, k)
		}
	}
	c.Env = w.Env

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
// the offending block. requireModel switches on the team-vs-role
// asymmetry: team `llm:` MUST declare `model:`; role `llm:` MAY omit
// it to inherit team's model.
func buildLLM(w *llmWire, ctx string, requireModel bool) (*LLMConfig, error) {
	if requireModel && w.Model == "" {
		return nil, fmt.Errorf("%w: %s.model empty", ErrInvalidModel, ctx)
	}
	if w.MaxOutputTokens != nil && *w.MaxOutputTokens < 0 {
		return nil, fmt.Errorf("%w: %s.max_output_tokens=%d (must be >= 0)", ErrInvalidLLMParam, ctx, *w.MaxOutputTokens)
	}
	if w.MaxContextTokens != nil && *w.MaxContextTokens < 0 {
		return nil, fmt.Errorf("%w: %s.max_context_tokens=%d (must be >= 0)", ErrInvalidLLMParam, ctx, *w.MaxContextTokens)
	}
	if w.Temperature != nil && (*w.Temperature < 0 || *w.Temperature > 2) {
		return nil, fmt.Errorf("%w: %s.temperature=%v (must be in [0,2])", ErrInvalidLLMParam, ctx, *w.Temperature)
	}
	if w.TopP != nil && (*w.TopP < 0 || *w.TopP > 1) {
		return nil, fmt.Errorf("%w: %s.top_p=%v (must be in [0,1])", ErrInvalidLLMParam, ctx, *w.TopP)
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

// buildIssues lifts the optional `issues:` block. When the block is
// absent every switch gets its default; a present-but-empty block
// receives the same defaults so `issues: {}` is equivalent to omission.
func buildIssues(w *issuesWire) *IssuesConfig {
	cfg := &IssuesConfig{DeleteBranchOnMerge: true}
	if w != nil && w.DeleteBranchOnMerge != nil {
		cfg.DeleteBranchOnMerge = *w.DeleteBranchOnMerge
	}
	return cfg
}

// buildRole validates and lifts one role from its parsed front matter.
// The role key is passed in (it is the `.md` filename) so error messages
// can include it. The prompt is NOT here — ParseAgentFile sets it from
// the Markdown body after the front matter.
func buildRole(key string, w *roleWire) (*Role, error) {
	triggers, err := buildTriggers(key, &w.Triggers)
	if err != nil {
		return nil, err
	}

	var roleLLM *LLMConfig
	if w.LLM != nil {
		llm, err := buildLLM(w.LLM, "roles."+key+".llm", false)
		if err != nil {
			return nil, err
		}
		roleLLM = llm
	}

	var scope Scope
	if w.Scope != nil {
		scope.Paths = w.Scope.Paths
	}

	// Tools: list of reusable rule names referenced by this role. Each
	// must be non-empty; existence in the host's `tools:` map is checked
	// at assembly time (LoadHostConfig), where the map is available.
	for i, n := range w.Tools {
		if strings.TrimSpace(n) == "" {
			return nil, fmt.Errorf("roles.%s.tools[%d]: empty rule name", key, i)
		}
	}

	// Permission: GitHub-style repo access level. Empty defaults to
	// "read" (fail-safe). Only "read"/"write" are accepted.
	permission := strings.TrimSpace(strings.ToLower(w.Permission))
	switch permission {
	case "":
		permission = "read"
	case "read", "write":
		// ok
	default:
		return nil, fmt.Errorf("roles.%s.permission=%q: %w", key, w.Permission, ErrInvalidPermission)
	}

	// Deduplicate and validate the MCP whitelist. Empty strings are
	// rejected; duplicates are collapsed (first occurrence kept).
	// We do NOT validate that server names exist in .mcp.json here
	// — the agent-runtime owns that check at session startup so
	// the error carries the exact missing server name.
	mcp := deduplicateStrings(w.Mcp)
	for i, name := range mcp {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("roles.%s.mcp[%d]: %w", key, i, ErrInvalidMCP)
		}
	}

	return &Role{
		Triggers:   triggers,
		Permission: permission,
		Tools:      append([]string(nil), w.Tools...),
		Scope:      scope,
		MCP:        mcp,
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
		if err := value.Decode(&cw); err != nil {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w: %s", roleKey, t, ErrInvalidTriggerSpec, err.Error())
		}
		return &TriggerSpec{Comment: &CommentFilter{
			MentionedOnly: cw.MentionedOnly,
			FromRoles:     cw.FromRoles,
			FromUsers:     cw.FromUsers,
		}}, nil

	case TriggerCommitPushed, TriggerPatchSubmitted:
		if emptyBlock {
			return &TriggerSpec{Push: &PushFilter{}}, nil
		}
		if value.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("roles.%s.triggers.%s: %w (filter must be a mapping)", roleKey, t, ErrInvalidTriggerSpec)
		}
		var pw pushFilterWire
		if err := value.Decode(&pw); err != nil {
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

// deduplicateStrings returns a copy of ss with duplicates removed,
// keeping the first occurrence. Empty input yields nil.
func deduplicateStrings(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, 0, len(ss))
	seen := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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

