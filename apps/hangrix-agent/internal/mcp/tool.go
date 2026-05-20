package mcp

import (
	"context"
	"encoding/json"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// mcpTool adapts a single MCP tool (name + description + JSON schema from
// the remote server) into the local.Tool interface. CallTool is forwarded
// through the MCP client.
type mcpTool struct {
	name        string
	description string
	schema      map[string]any
	client      *mcpclient.Client
}

func (t *mcpTool) Name() string           { return t.name }
func (t *mcpTool) Description() string    { return t.description }
func (t *mcpTool) Schema() map[string]any { return t.schema }

func (t *mcpTool) Call(ctx context.Context, args json.RawMessage) (any, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = t.name

	if len(args) > 0 {
		var a map[string]any
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		req.Params.Arguments = a
	}

	result, err := t.client.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.IsError {
		errText := ""
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				errText += tc.Text
			}
		}
		return map[string]any{"is_error": true, "text": errText}, nil
	}
	var texts []string
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	if len(texts) == 1 {
		return texts[0], nil
	}
	return texts, nil
}

// convertSchema translates an mcp.Tool's InputSchema into the
// map[string]any shape our registry expects.
func convertSchema(t mcp.Tool) map[string]any {
	s := map[string]any{
		"type": t.InputSchema.Type,
	}
	if t.InputSchema.Properties != nil {
		s["properties"] = t.InputSchema.Properties
	}
	if len(t.InputSchema.Required) > 0 {
		s["required"] = t.InputSchema.Required
	}
	if t.InputSchema.AdditionalProperties != nil {
		s["additionalProperties"] = t.InputSchema.AdditionalProperties
	}
	return s
}
