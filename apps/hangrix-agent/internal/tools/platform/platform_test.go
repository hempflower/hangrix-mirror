package platform

import (
	"reflect"
	"testing"
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
