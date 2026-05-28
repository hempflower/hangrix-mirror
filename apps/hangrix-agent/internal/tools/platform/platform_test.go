package platform

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
)

func TestIssueEditSchemaIsUpstreamCompatible(t *testing.T) {
	schema := issueEditSchema()
	assertSchemaShape(t, "issue_edit", schema)

	if got := schema["type"]; got != "object" {
		t.Fatalf("issueEditSchema type = %v, want %q", got, "object")
	}

	if got := schema["minProperties"]; got != 1 {
		t.Fatalf("issueEditSchema minProperties = %v, want 1", got)
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("issueEditSchema properties has unexpected type %T", schema["properties"])
	}

	if _, ok := props["title"]; !ok {
		t.Fatal("issueEditSchema properties missing title")
	}
	if _, ok := props["body"]; !ok {
		t.Fatal("issueEditSchema properties missing body")
	}
}

func TestAllPlatformToolSchemasAreUpstreamCompatible(t *testing.T) {
	client := NewClient("https://example.invalid", "token")
	tools := All(client, nil, false)

	if len(tools) == 0 {
		t.Fatal("All returned no platform tools")
	}

	for _, tool := range tools {
		t.Run(tool.Name(), func(t *testing.T) {
			assertSchemaShape(t, tool.Name(), tool.Schema())
		})
	}
}

func assertSchemaShape(t *testing.T, toolName string, schema map[string]any) {
	t.Helper()

	if schema == nil {
		t.Fatalf("%s schema is nil", toolName)
	}

	if got := schema["type"]; got != "object" {
		t.Fatalf("%s schema type = %v, want %q", toolName, got, "object")
	}

	for _, key := range []string{"oneOf", "anyOf", "allOf", "enum", "not"} {
		if _, ok := schema[key]; ok {
			t.Fatalf("%s schema contains unsupported top-level keyword %q: %#v", toolName, key, schema)
		}
	}

	props, hasProps := schema["properties"].(map[string]any)
	if !hasProps {
		return
	}

	required, hasRequired := schema["required"].([]string)
	if !hasRequired {
		return
	}

	for _, field := range required {
		if _, ok := props[field]; !ok {
			t.Fatalf("%s schema required field %q missing from properties", toolName, field)
		}
	}

	for field, fieldSchema := range props {
		fieldMap, ok := fieldSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s schema property %q has invalid schema shape %T", toolName, field, fieldSchema)
		}
		if _, ok := fieldMap["type"]; !ok {
			t.Fatalf("%s schema property %q is missing type", toolName, field)
		}
		if _, ok := fieldMap["description"]; !ok {
			t.Fatalf("%s schema property %q is missing description", toolName, field)
		}
	}

	if !reflect.DeepEqual(required, schema["required"].([]string)) {
		t.Fatalf("%s schema required slice changed unexpectedly", toolName)
	}
}

func TestDecodeV1ErrorWithDetails(t *testing.T) {
	// Simulate a server 422 response with FieldError details — the
	// "body too long" case from issue #202.
	body := []byte(`{
		"message": "comment body too long: 4517 runes (limit 4000)",
		"errors": [
			{
				"resource": "comment",
				"field": "body",
				"code": "too_long",
				"message": "body has 4517 Unicode characters; the maximum is 4000. Split the content into multiple ` + "`" + `issue_comment` + "`" + ` calls, each ≤4000 characters. Prefix each segment with ` + "`" + `[1/N]` + "`" + `, ` + "`" + `[2/N]` + "`" + `, … so readers can follow the sequence."
			}
		]
	}`)

	err := decodeV1Error(422, "/issues/current/comments", body)
	if err == nil {
		t.Fatal("decodeV1Error returned nil, expected error")
	}

	raw := err.Error()
	if !strings.HasPrefix(raw, "{") {
		t.Fatalf("decodeV1Error output does not look like JSON: %s", raw)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("decodeV1Error output is not valid JSON: %v\nraw: %s", err, raw)
	}

	if got, ok := parsed["is_error"]; !ok || got != true {
		t.Fatalf("is_error = %v, want true", got)
	}
	if got := parsed["status"]; got != float64(422) {
		t.Fatalf("status = %v, want 422", got)
	}
	if got := parsed["error"]; got == nil || got == "" {
		t.Fatal("error field is missing or empty")
	}

	details, ok := parsed["details"]
	if !ok {
		t.Fatal("expected 'details' key when errors[] is present, but it's missing")
	}
	detailsArr, ok := details.([]interface{})
	if !ok {
		t.Fatalf("details is not an array: %T", details)
	}
	if len(detailsArr) != 1 {
		t.Fatalf("details array has %d elements, want 1", len(detailsArr))
	}
	first, ok := detailsArr[0].(map[string]any)
	if !ok {
		t.Fatalf("details[0] has unexpected type %T", detailsArr[0])
	}
	if got := first["code"]; got != "too_long" {
		t.Fatalf("details[0].code = %v, want %q", got, "too_long")
	}
	if msg, ok := first["message"].(string); !ok || !strings.Contains(msg, "Split") {
		t.Fatalf("details[0].message should contain 'Split', got %q", first["message"])
	}
}

func TestDecodeV1ErrorWithoutDetails(t *testing.T) {
	// Simulate a server 422 response without errors[] — the "body is
	// required" case (missing field).
	body := []byte(`{"message": "body is required"}`)

	err := decodeV1Error(422, "/issues/current/comments", body)
	if err == nil {
		t.Fatal("decodeV1Error returned nil, expected error")
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(err.Error()), &parsed); err != nil {
		t.Fatalf("decodeV1Error output is not valid JSON: %v", err)
	}

	if _, ok := parsed["details"]; ok {
		t.Fatal("expected no 'details' key when errors[] is absent, but it's present")
	}
}

func TestIssueCommentSchemaBodyMaxLengthAndSplitHint(t *testing.T) {
	client := NewClient("https://example.invalid", "token")
	tools := All(client, nil, false)

	var issueComment local.Tool
	for _, tool := range tools {
		if tool.Name() == "issue_comment" {
			issueComment = tool
			break
		}
	}
	if issueComment == nil {
		t.Fatal("issue_comment tool not found in All()")
	}

	schema := issueComment.Schema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("issue_comment schema properties not found or wrong type: %T", schema["properties"])
	}

	bodySchema, ok := props["body"].(map[string]any)
	if !ok {
		t.Fatalf("issue_comment schema body property not found or wrong type: %T", props["body"])
	}

	maxLength, ok := bodySchema["maxLength"]
	if !ok {
		t.Fatal("issue_comment body schema is missing maxLength")
	}
	if ml, ok := maxLength.(int); !ok || ml != 7800 {
		t.Fatalf("issue_comment body maxLength = %v (%T), want 7800", maxLength, maxLength)
	}

	desc, ok := bodySchema["description"].(string)
	if !ok {
		t.Fatalf("issue_comment body description not found or wrong type: %T", bodySchema["description"])
	}
	if !strings.Contains(desc, "split") {
		t.Fatalf("issue_comment body description missing 'split': %s", desc)
	}
	if !strings.Contains(desc, "[1/N]") {
		t.Fatalf("issue_comment body description missing '[1/N]': %s", desc)
	}
}

func TestAskQuestionSchemaTextMaxLength(t *testing.T) {
	schema := askQuestionSchema()
	questions, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("askQuestionSchema properties not found or wrong type: %T", schema["properties"])
	}
	questionsSchema, ok := questions["questions"].(map[string]any)
	if !ok {
		t.Fatalf("questions property not found or wrong type: %T", questions["questions"])
	}
	items, ok := questionsSchema["items"].(map[string]any)
	if !ok {
		t.Fatalf("questions.items not found or wrong type: %T", questionsSchema["items"])
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("questions.items.properties not found or wrong type: %T", items["properties"])
	}
	textSchema, ok := itemProps["text"].(map[string]any)
	if !ok {
		t.Fatalf("questions.items.properties.text not found or wrong type: %T", itemProps["text"])
	}

	maxLength, ok := textSchema["maxLength"]
	if !ok {
		t.Fatal("question text schema is missing maxLength")
	}
	if ml, ok := maxLength.(int); !ok || ml != 300 {
		t.Fatalf("question text maxLength = %v (%T), want 300", maxLength, maxLength)
	}

	desc, ok := textSchema["description"].(string)
	if !ok {
		t.Fatalf("question text description not found or wrong type: %T", textSchema["description"])
	}
	if !strings.Contains(desc, "1-300") {
		t.Fatalf("question text description missing '1-300': %s", desc)
	}
	if !strings.Contains(desc, "concise") {
		t.Fatalf("question text description missing 'concise': %s", desc)
	}
}

func TestAskQuestionToolDescriptionHasGuidance(t *testing.T) {
	client := NewClient("https://example.invalid", "token")
	tools := All(client, nil, false)

	var askQuestionTool local.Tool
	for _, tool := range tools {
		if tool.Name() == "ask_question" {
			askQuestionTool = tool
			break
		}
	}
	if askQuestionTool == nil {
		t.Fatal("ask_question tool not found in All()")
	}

	desc := askQuestionTool.Description()
	if !strings.Contains(desc, "\u2264300") {
		t.Fatalf("ask_question description missing '\u2264300': %s", desc)
	}
	if !strings.Contains(desc, "recommended") {
		t.Fatalf("ask_question description missing 'recommended': %s", desc)
	}
}
