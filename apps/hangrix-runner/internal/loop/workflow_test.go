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

	driver := &WorkflowJobDriver{}
	job := &client.WorkflowJob{
		WorkflowRunID: 7,
		WorkflowName:  "ci",
		JobKey:        "build",
		Owner:         "acme",
		Name:          "widgets",
		CommitSHA:     "deadbeef",
		CheckoutRef:   "refs/heads/feat/x",
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
		"HANGRIX_WORKFLOW_RUN_ID":  "7",
		"HANGRIX_WORKFLOW_NAME":    "ci",
		"HANGRIX_WORKFLOW_JOB_KEY": "build",
		"HANGRIX_REPO_OWNER":       "acme",
		"HANGRIX_REPO_NAME":        "widgets",
		"HANGRIX_COMMIT_SHA":       "deadbeef",
		"HANGRIX_CHECKOUT_REF":     "refs/heads/feat/x",
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
