package handler

import "testing"

func TestStepKeyFromRequest(t *testing.T) {
	tests := []struct {
		name      string
		stepID    string
		stepIndex int
		want      string
	}{
		{
			name:      "explicit step_id takes precedence",
			stepID:    "build",
			stepIndex: 2,
			want:      "build",
		},
		{
			name:      "fallback: first unnamed step (index 0 → key \"1\")",
			stepID:    "",
			stepIndex: 0,
			want:      "1",
		},
		{
			name:      "fallback: second unnamed step (index 1 → key \"2\")",
			stepID:    "",
			stepIndex: 1,
			want:      "2",
		},
		{
			name:      "fallback: third unnamed step (index 2 → key \"3\")",
			stepID:    "",
			stepIndex: 2,
			want:      "3",
		},
		{
			name:      "explicit step_id even when step_index is 0",
			stepID:    "test",
			stepIndex: 0,
			want:      "test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stepKeyFromRequest(tt.stepID, tt.stepIndex)
			if got != tt.want {
				t.Errorf("stepKeyFromRequest(%q, %d) = %q, want %q",
					tt.stepID, tt.stepIndex, got, tt.want)
			}
		})
	}
}
