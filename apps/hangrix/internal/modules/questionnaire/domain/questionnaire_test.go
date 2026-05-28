package domain

import (
	"strings"
	"testing"
)

func TestCreateParams_Validate_QuestionTextLength(t *testing.T) {
	tests := []struct {
		name      string
		textLen   int
		wantError bool
	}{
		{"299 chars — allowed", 299, false},
		{"300 chars — allowed (boundary)", 300, false},
		{"301 chars — rejected", 301, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := strings.Repeat("a", tt.textLen)
			p := &CreateParams{
				Title:   "Test",
				Questions: []CreateQuestion{
					{Position: 0, Text: text, Type: QtypeTextInput, Required: true},
				},
			}
			errs := p.Validate()
			hasTooLong := false
			for _, e := range errs {
				if e.Code == "too_long" {
					hasTooLong = true
					break
				}
			}
			if tt.wantError && !hasTooLong {
				t.Errorf("expected a too_long error for %d-char text, got none (errors: %v)", tt.textLen, errs)
			}
			if !tt.wantError && hasTooLong {
				t.Errorf("expected no too_long error for %d-char text, but got one", tt.textLen)
			}
		})
	}
}
