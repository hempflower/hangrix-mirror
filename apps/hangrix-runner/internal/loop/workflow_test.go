package loop

import (
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/client"
	"github.com/hangrix/hangrix/apps/hangrix-runner/internal/orchestrator"
)

func TestBuildWorkflowEnv_ExpandEnv(t *testing.T) {
	// Regression: buildWorkflowEnv was not calling expandEnv, so ${VAR_NAME}
	// references in env vars were never expanded at exec time. This test
	// verifies that expansion happens and produces the expected values.

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "test",
		Owner:         "acme",
		Name:          "repo",
		CommitSHA:     "abc1234",
		CheckoutRef:   "refs/heads/main",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env: map[string]string{
				"SECRET":  "${MY_SECRET}",
				"PLAIN":   "hello",
				"LITERAL": "plain-value",
			},
		},
		RepoVariables: map[string]string{
			"MY_SECRET": "sk-xyz",
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expanded ${MY_SECRET} reference.
	if got, want := env["SECRET"], "sk-xyz"; got != want {
		t.Errorf("SECRET = %q, want %q", got, want)
	}
	// Plain value passes through.
	if got, want := env["PLAIN"], "hello"; got != want {
		t.Errorf("PLAIN = %q, want %q", got, want)
	}
	// Non-reference passes through unchanged.
	if got, want := env["LITERAL"], "plain-value"; got != want {
		t.Errorf("LITERAL = %q, want %q", got, want)
	}
}

func TestBuildWorkflowEnv_UndefinedVarError(t *testing.T) {
	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env: map[string]string{
				"KEY": "${MISSING}",
			},
		},
		RepoVariables: map[string]string{},
	}

	_, err := driver.buildWorkflowEnv(job)
	if err == nil {
		t.Fatal("expected error for undefined variable, got nil")
	}
	if !contains(err.Error(), `"MISSING"`) {
		t.Errorf("error %q should mention MISSING", err.Error())
	}
}

func TestBuildWorkflowEnv_EventNameAndCauseID(t *testing.T) {
	// Regression: HANGRIX_EVENT_NAME and HANGRIX_EVENT_CAUSE_ID were not
	// injected by buildWorkflowEnv before the fix.

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 42,
		WorkflowName:  "deploy",
		JobKey:        "ship",
		Owner:         "myorg",
		Name:          "myrepo",
		EventName:     "repo.push",
		EventCauseID:  "evt-1234",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
		RepoVariables: map[string]string{},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := env["HANGRIX_EVENT_NAME"], "repo.push"; got != want {
		t.Errorf("HANGRIX_EVENT_NAME = %q, want %q", got, want)
	}
	if got, want := env["HANGRIX_EVENT_CAUSE_ID"], "evt-1234"; got != want {
		t.Errorf("HANGRIX_EVENT_CAUSE_ID = %q, want %q", got, want)
	}
}

func TestBuildWorkflowEnv_EventFieldsOmittedWhenEmpty(t *testing.T) {
	// When EventName / EventCauseID are empty, the env vars should not be
	// injected at all (omitempty on the struct, empty-string guard in
	// buildWorkflowEnv).

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "lint",
		Owner:         "org",
		Name:          "repo",
		EventName:     "", // empty
		EventCauseID:  "", // empty
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := env["HANGRIX_EVENT_NAME"]; ok {
		t.Error("HANGRIX_EVENT_NAME should not be set when EventName is empty")
	}
	if _, ok := env["HANGRIX_EVENT_CAUSE_ID"]; ok {
		t.Error("HANGRIX_EVENT_CAUSE_ID should not be set when EventCauseID is empty")
	}
}

func TestBuildWorkflowEnv_Tag(t *testing.T) {
	// Verify HANGRIX_TAG is injected when Tag is set (repo.push_tag event).

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 42,
		WorkflowName:  "deploy",
		JobKey:        "ship",
		Owner:         "myorg",
		Name:          "myrepo",
		EventName:     "repo.push_tag",
		Tag:           "v1.2.3",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
		RepoVariables: map[string]string{},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := env["HANGRIX_TAG"], "v1.2.3"; got != want {
		t.Errorf("HANGRIX_TAG = %q, want %q", got, want)
	}
}

func TestBuildWorkflowEnv_TagOmittedWhenEmpty(t *testing.T) {
	// When Tag is empty, HANGRIX_TAG should not be injected at all.

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "lint",
		Owner:         "org",
		Name:          "repo",
		Tag:           "", // empty
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := env["HANGRIX_TAG"]; ok {
		t.Error("HANGRIX_TAG should not be set when Tag is empty")
	}
}


func TestBuildWorkflowEnv_WorkflowInputs(t *testing.T) {
	// Dispatch inputs are pre-transformed by the server to WORKFLOW_INPUT_*
	// keys and injected as-is.

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "deploy",
		JobKey:        "ship",
		Owner:         "org",
		Name:          "repo",
		Inputs: map[string]string{
			"WORKFLOW_INPUT_REF":    "main",
			"WORKFLOW_INPUT_DRY_RUN": "true",
		},
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := env["WORKFLOW_INPUT_REF"], "main"; got != want {
		t.Errorf("WORKFLOW_INPUT_REF = %q, want %q", got, want)
	}
	if got, want := env["WORKFLOW_INPUT_DRY_RUN"], "true"; got != want {
		t.Errorf("WORKFLOW_INPUT_DRY_RUN = %q, want %q", got, want)
	}
}

func TestBuildWorkflowEnv_PlatformRuntimeVars(t *testing.T) {
	// Verify all the mandatory HANGRIX_* runtime vars are present.

	driver := &WorkflowJobDriver{BaseURL: "https://hangrix.example.com"}
	job := &client.WorkflowJob{
		WorkflowRunID: 7,
		WorkflowName:  "ci",
		JobKey:        "build",
		Owner:         "acme",
		Name:          "widgets",
		CommitSHA:     "deadbeef",
		CheckoutRef:   "refs/heads/feat/x",
		WorkflowToken: "hgxw_secret",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := map[string]string{
		"HANGRIX_WORKFLOW_RUN_ID":    "7",
		"HANGRIX_WORKFLOW_NAME":      "ci",
		"HANGRIX_WORKFLOW_JOB_KEY":   "build",
		"HANGRIX_REPO_OWNER":         "acme",
		"HANGRIX_REPO_NAME":          "widgets",
		"HANGRIX_COMMIT_SHA":         "deadbeef",
		"HANGRIX_CHECKOUT_REF":       "refs/heads/feat/x",
		"HANGRIX_PLATFORM_BASE_URL":  "https://hangrix.example.com",
		"HANGRIX_WORKFLOW_TOKEN":     "hgxw_secret",
	}
	for key, want := range tests {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestBuildWorkflowEnv_CommitSHAAndCheckoutRefOmittedWhenEmpty(t *testing.T) {
	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "lint",
		Owner:         "org",
		Name:          "repo",
		CommitSHA:     "",
		CheckoutRef:   "",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := env["HANGRIX_COMMIT_SHA"]; ok {
		t.Error("HANGRIX_COMMIT_SHA should not be set when CommitSHA is empty")
	}
	if _, ok := env["HANGRIX_CHECKOUT_REF"]; ok {
		t.Error("HANGRIX_CHECKOUT_REF should not be set when CheckoutRef is empty")
	}
}

func TestRun_EarlyValidationDoesNotMutateContainerEnv(t *testing.T) {
	// Regression: the early validation in Run used to call expandEnv on
	// job.Container.Env directly, which mutated the original map. The fix
	// copies to a temporary map first. This test verifies that after Run's
	// validation path, job.Container.Env remains unchanged.

	driver := &WorkflowJobDriver{}

	originalEnv := map[string]string{
		"SECRET": "${MY_SECRET}",
		"PLAIN":  "hello",
	}
	repoVars := map[string]string{
		"MY_SECRET": "sk-decrypted",
	}

	job := &client.WorkflowJob{
		JobRunID: 1,
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   originalEnv,
		},
		RepoVariables: repoVars,
	}

	// Simulate the early validation path from Run.  We can't call Run
	// directly without real network + orchestrator, but the validation
	// block is a standalone snippet: it copies, expands, and discards.
	tmp := make(map[string]string, len(job.Container.Env))
	for k, v := range job.Container.Env {
		tmp[k] = v
	}
	if err := expandEnv(tmp, job.RepoVariables); err != nil {
		t.Fatalf("expandEnv: %v", err)
	}

	// The original env must be unchanged (still contains ${MY_SECRET}).
	if got, want := job.Container.Env["SECRET"], "${MY_SECRET}"; got != want {
		t.Errorf("env mutated: SECRET = %q, want %q", got, want)
	}
	if got, want := job.Container.Env["PLAIN"], "hello"; got != want {
		t.Errorf("env mutated: PLAIN = %q, want %q", got, want)
	}

	// Sanity: the temp copy was expanded.
	if got, want := tmp["SECRET"], "sk-decrypted"; got != want {
		t.Errorf("tmp was not expanded: SECRET = %q, want %q", got, want)
	}

	// Now also verify that buildWorkflowEnv internally copies+expands
	// without mutating the original.
	_, _ = driver.buildWorkflowEnv(job)
	if got, want := job.Container.Env["SECRET"], "${MY_SECRET}"; got != want {
		t.Errorf("after buildWorkflowEnv, env mutated: SECRET = %q, want %q", got, want)
	}
}

func TestBuildWorkflowEnv_EmptyContainerEnv(t *testing.T) {
	// buildWorkflowEnv must not panic on nil/empty container env.
	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "lint",
		Owner:         "org",
		Name:          "repo",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   nil,
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env["HANGRIX_WORKFLOW_RUN_ID"] != "1" {
		t.Error("platform runtime vars missing when container env is nil")
	}
}

func TestBuildWorkflowEnv_ExpandEnvNilRepoVars(t *testing.T) {
	// When RepoVariables is nil (server hasn't upgraded), ${VAR} refs
	// should pass through unexpanded without error.
	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "lint",
		Owner:         "org",
		Name:          "repo",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env: map[string]string{
				"SECRET": "${MY_SECRET}",
			},
		},
		RepoVariables: nil, // backward-compat: no expansion
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := env["SECRET"], "${MY_SECRET}"; got != want {
		t.Errorf("SECRET = %q, want %q (should pass through unexpanded)", got, want)
	}
}


func TestOrchestratorVolumes(t *testing.T) {
	tests := []struct {
		name   string
		vols   []client.Volume
		repoID int64
		want   []orchestrator.Volume
	}{
		{
			name:   "nil input",
			vols:   nil,
			repoID: 0,
			want:   nil,
		},
		{
			name:   "empty slice",
			vols:   []client.Volume{},
			repoID: 0,
			want:   nil,
		},
		{
			name: "no prefix when repoID=0",
			vols: []client.Volume{
				{Name: "npm-cache", Mount: "/root/.npm"},
			},
			repoID: 0,
			want: []orchestrator.Volume{
				{Name: "npm-cache", Mount: "/root/.npm"},
			},
		},
		{
			name: "prefixes with repo-{id}- when repoID>0",
			vols: []client.Volume{
				{Name: "npm-cache", Mount: "/root/.npm"},
			},
			repoID: 6,
			want: []orchestrator.Volume{
				{Name: "repo-6-npm-cache", Mount: "/root/.npm"},
			},
		},
		{
			name: "multiple volumes with prefix",
			vols: []client.Volume{
				{Name: "go-cache", Mount: "/root/.cache/go-build"},
				{Name: "mod-cache", Mount: "/go/pkg/mod"},
			},
			repoID: 6,
			want: []orchestrator.Volume{
				{Name: "repo-6-go-cache", Mount: "/root/.cache/go-build"},
				{Name: "repo-6-mod-cache", Mount: "/go/pkg/mod"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := orchestratorVolumes(tt.vols, tt.repoID)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStepOutputPath_WithID(t *testing.T) {
	step := client.WorkflowStep{ID: "create-release", Name: "Create release", Run: "echo done"}
	got := stepOutputPath(step, 0)
	want := "/tmp/hangrix/step-output-create-release"
	if got != want {
		t.Errorf("stepOutputPath = %q, want %q", got, want)
	}
}

func TestStepOutputPath_FallbackToIndex(t *testing.T) {
	step := client.WorkflowStep{Name: "Lint", Run: "gofmt -w ."}
	got := stepOutputPath(step, 3) // 0-based → 1-based "4"
	want := "/tmp/hangrix/step-output-4"
	if got != want {
		t.Errorf("stepOutputPath = %q, want %q", got, want)
	}
}

func TestParseOutputLines_Empty(t *testing.T) {
	if got := parseOutputLines(""); got != nil {
		t.Errorf("parseOutputLines(\"\") = %v, want nil", got)
	}
	if got := parseOutputLines("  \n  "); got != nil {
		t.Errorf("parseOutputLines whitespace = %v, want nil", got)
	}
}

func TestParseOutputLines_SingleLine(t *testing.T) {
	out := parseOutputLines("release_id=42\n")
	if got, want := out["release_id"], "42"; got != want {
		t.Errorf("release_id = %q, want %q", got, want)
	}
	if len(out) != 1 {
		t.Errorf("len = %d, want 1", len(out))
	}
}

func TestParseOutputLines_MultipleLines(t *testing.T) {
	out := parseOutputLines("foo=bar\n  baz = qux  \nhello=world\n")
	if got, want := out["foo"], "bar"; got != want {
		t.Errorf("foo = %q, want %q", got, want)
	}
	if got, want := out["baz"], "qux"; got != want {
		t.Errorf("baz = %q, want %q", got, want)
	}
	if got, want := out["hello"], "world"; got != want {
		t.Errorf("hello = %q, want %q", got, want)
	}
	if len(out) != 3 {
		t.Errorf("len = %d, want 3", len(out))
	}
}

func TestParseOutputLines_SkipsInvalid(t *testing.T) {
	// Lines without '=', keys that are empty, and blank lines are skipped.
	// Lines with valid keys and empty values (e.g. "keyonly=") are kept.
	out := parseOutputLines("noequals\n=valueonly\nkeyonly=\nvalid=1\n\n  \n")
	if got, want := out["valid"], "1"; got != want {
		t.Errorf("valid = %q, want %q", got, want)
	}
	if got, want := out["keyonly"], ""; got != want {
		t.Errorf("keyonly = %q, want %q", got, want)
	}
	if len(out) != 2 {
		t.Errorf("len = %d, want 2; got %v", len(out), out)
	}
}

func TestParseOutputLines_ValuesWithEquals(t *testing.T) {
	// The value may contain '='; split only on the first one.
	out := parseOutputLines("url=https://example.com?a=1&b=2\n")
	if got, want := out["url"], "https://example.com?a=1&b=2"; got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestMaskSecretValues_NoMatch(t *testing.T) {
	outputs := map[string]string{"version": "1.2.3", "count": "42"}
	secrets := map[string]string{"MY_SECRET": "sk-abc123"}
	masked := maskSecretValues(outputs, secrets)
	if len(masked) != 0 {
		t.Errorf("masked = %v, want empty", masked)
	}
}

func TestMaskSecretValues_Match(t *testing.T) {
	outputs := map[string]string{"api_key": "sk-abc123", "version": "1.0.0"}
	secrets := map[string]string{"OPENAI_KEY": "sk-abc123"}
	masked := maskSecretValues(outputs, secrets)
	if len(masked) != 1 || masked[0] != "api_key" {
		t.Errorf("masked = %v, want [api_key]", masked)
	}
}

func TestMaskSecretValues_MultipleMatches(t *testing.T) {
	outputs := map[string]string{
		"token":    "ghp_secret",
		"endpoint": "https://api.example.com",
		"key":      "sk-other",
	}
	secrets := map[string]string{
		"GH_TOKEN":   "ghp_secret",
		"OPENAI_KEY": "sk-other",
	}
	masked := maskSecretValues(outputs, secrets)
	if len(masked) != 2 {
		t.Errorf("masked len = %d, want 2; got %v", len(masked), masked)
	}
	// Order not guaranteed; check presence.
	has := make(map[string]bool)
	for _, k := range masked {
		has[k] = true
	}
	if !has["token"] {
		t.Error("token should be masked")
	}
	if !has["key"] {
		t.Error("key should be masked")
	}
}

func TestMaskSecretValues_EmptyInputs(t *testing.T) {
	if got := maskSecretValues(nil, map[string]string{"S": "v"}); got != nil {
		t.Errorf("nil outputs: got %v, want nil", got)
	}
	if got := maskSecretValues(map[string]string{}, map[string]string{"S": "v"}); got != nil {
		t.Errorf("empty outputs: got %v, want nil", got)
	}
	if got := maskSecretValues(map[string]string{"k": "v"}, nil); got != nil {
		t.Errorf("nil secrets: got %v, want nil", got)
	}
	if got := maskSecretValues(map[string]string{"k": "v"}, map[string]string{}); got != nil {
		t.Errorf("empty secrets: got %v, want nil", got)
	}
}

func TestMaskSecretValues_SkipsEmptySecretValues(t *testing.T) {
	outputs := map[string]string{"key": ""}
	secrets := map[string]string{"EMPTY_SECRET": ""}
	masked := maskSecretValues(outputs, secrets)
	if len(masked) != 0 {
		t.Errorf("masked = %v, want empty (empty secret values should not match)", masked)
	}
}

func TestBuildWorkflowEnv_StepOutputFileInjected(t *testing.T) {
	// Verify that runStep injects HANGRIX_STEP_OUTPUT_FILE into the env.
	// We can't call runStep without a real orchestrator, but we can verify
	// the path derivation via stepOutputPath, which is what runStep uses.
	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "build",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	// Build the base env.
	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate what runStep does: inject HANGRIX_STEP_OUTPUT_FILE.
	step := client.WorkflowStep{ID: "build", Run: "make"}
	env["HANGRIX_STEP_OUTPUT_FILE"] = stepOutputPath(step, 0)

	if got, want := env["HANGRIX_STEP_OUTPUT_FILE"], "/tmp/hangrix/step-output-build"; got != want {
		t.Errorf("HANGRIX_STEP_OUTPUT_FILE = %q, want %q", got, want)
	}
}

func TestExpandStepOutputRefs_NoOutputs(t *testing.T) {
	// When no outputs are accumulated, text passes through unchanged.
	text := "echo ${{ steps.build.outputs.version }}"
	got, err := expandStepOutputRefs(text, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != text {
		t.Errorf("got %q, want %q", got, text)
	}

	got, err = expandStepOutputRefs(text, map[string]map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != text {
		t.Errorf("got %q, want %q", got, text)
	}
}

func TestExpandStepOutputRefs_NoReferences(t *testing.T) {
	text := "echo hello world"
	outputs := map[string]map[string]string{
		"build": {"version": "1.2.3"},
	}
	got, err := expandStepOutputRefs(text, outputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != text {
		t.Errorf("got %q, want %q", got, text)
	}
}

func TestExpandStepOutputRefs_SingleReference(t *testing.T) {
	text := "echo Version is ${{ steps.build.outputs.version }}"
	outputs := map[string]map[string]string{
		"build": {"version": "1.2.3"},
	}
	got, err := expandStepOutputRefs(text, outputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "echo Version is 1.2.3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandStepOutputRefs_MultipleReferences(t *testing.T) {
	text := "echo ${{ steps.build.outputs.version }}-${{ steps.build.outputs.commit }}"
	outputs := map[string]map[string]string{
		"build": {"version": "1.2.3", "commit": "abc1234"},
	}
	got, err := expandStepOutputRefs(text, outputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "echo 1.2.3-abc1234"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandStepOutputRefs_CrossStepReferences(t *testing.T) {
	text := "release_id=${{ steps.create-release.outputs.release_id }}"
	outputs := map[string]map[string]string{
		"create-release": {"release_id": "42"},
		"build":          {"version": "1.0.0"},
	}
	got, err := expandStepOutputRefs(text, outputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "release_id=42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandStepOutputRefs_StepNotFound(t *testing.T) {
	text := "echo ${{ steps.build.outputs.version }}"
	outputs := map[string]map[string]string{
		"lint": {"issues": "0"},
	}
	_, err := expandStepOutputRefs(text, outputs)
	if err == nil {
		t.Fatal("expected error for missing step, got nil")
	}
	if !contains(err.Error(), `"build"`) {
		t.Errorf("error %q should mention step build", err.Error())
	}
}

func TestExpandStepOutputRefs_KeyNotFound(t *testing.T) {
	text := "echo ${{ steps.build.outputs.commit }}"
	outputs := map[string]map[string]string{
		"build": {"version": "1.2.3"},
	}
	_, err := expandStepOutputRefs(text, outputs)
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !contains(err.Error(), `"commit"`) {
		t.Errorf("error %q should mention key commit", err.Error())
	}
}

func TestExpandStepOutputRefs_WhitespaceInsensitive(t *testing.T) {
	// Spaces around the expression should be tolerated.
	text := "echo ${{  steps.build.outputs.version  }}"
	outputs := map[string]map[string]string{
		"build": {"version": "3.0.0"},
	}
	got, err := expandStepOutputRefs(text, outputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "echo 3.0.0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandStepOutputRefs_NoPartialMatch(t *testing.T) {
	// ${{ env.VAR }} or ${{ inputs.name }} should NOT be matched —
	// only steps.<id>.outputs.<key> is expanded by the runner.
	text := "echo ${{ env.MY_KEY }} and ${{ inputs.ref }}"
	outputs := map[string]map[string]string{
		"env":    {"MY_KEY": "secret"},
		"inputs": {"ref": "main"},
	}
	got, err := expandStepOutputRefs(text, outputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The env/inputs references should pass through unchanged.
	if got != text {
		t.Errorf("got %q, want %q (env/inputs should not be expanded by runner)", got, text)
	}
}

func TestBuildWorkflowEnv_StepOutputFileFallback(t *testing.T) {
	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 1,
		WorkflowName:  "ci",
		JobKey:        "lint",
		Container: client.WorkflowContainer{
			Image: "alpine:latest",
			Env:   map[string]string{},
		},
	}

	env, err := driver.buildWorkflowEnv(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step without an explicit ID falls back to 1-based index.
	step := client.WorkflowStep{Run: "echo hi"}
	env["HANGRIX_STEP_OUTPUT_FILE"] = stepOutputPath(step, 2) // third step

	if got, want := env["HANGRIX_STEP_OUTPUT_FILE"], "/tmp/hangrix/step-output-3"; got != want {
		t.Errorf("HANGRIX_STEP_OUTPUT_FILE = %q, want %q", got, want)
	}
}
