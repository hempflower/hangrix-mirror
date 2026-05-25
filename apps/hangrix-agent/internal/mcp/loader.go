package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hangrix/hangrix/apps/hangrix-agent/internal/tools/local"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// LoadServers connects to every MCP server defined in cfg, initializes
// each, lists its tools, and wraps them as local.Tool. Individual server
// failures are logged as warnings and skipped; the agent continues startup.
//
// Callers must eventually call Close() on every returned Client.
func LoadServers(ctx context.Context, cfg *Config, logf func(string, ...interface{}), httpClient *http.Client) ([]local.Tool, []*mcpclient.Client) {
	if cfg == nil || len(cfg.McpServers) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Give the entire load phase a generous timeout.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var (
		tools   []local.Tool
		clients []*mcpclient.Client
	)

	for name, s := range cfg.McpServers {
		// Per-server validation: bad config skips only this server.
		switch {
		case s.Command != "":
			// stdio — valid, proceed.
		case s.Type == "http", s.Type == "sse":
			if s.URL == "" {
				logf("WARN: skipping mcp server %q: type %q requires url", name, s.Type)
				continue
			}
		default:
			logf("WARN: skipping mcp server %q: must have either command or type (\"http\" / \"sse\")", name)
			continue
		}

		switch {
		case s.Command != "":
			t, c, err := loadStdio(ctx, name, s)
			if err != nil {
				logf("WARN: skipping mcp server %q: %v", name, err)
				continue
			}
			tools = append(tools, t...)
			clients = append(clients, c)

		case s.Type == "http":
			t, c, err := loadHTTP(ctx, name, s, httpClient)
			if err != nil {
				logf("WARN: skipping mcp server %q: %v", name, err)
				continue
			}
			tools = append(tools, t...)
			clients = append(clients, c)

		case s.Type == "sse":
			t, c, err := loadSSE(ctx, name, s, httpClient)
			if err != nil {
				logf("WARN: skipping mcp server %q: %v", name, err)
				continue
			}
			tools = append(tools, t...)
			clients = append(clients, c)
		}
	}
	return tools, clients
}

func loadStdio(ctx context.Context, name string, s ServerDef) ([]local.Tool, *mcpclient.Client, error) {
	c, err := mcpclient.NewStdioMCPClient(s.Command, nil, s.Args...)
	if err != nil {
		return nil, nil, fmt.Errorf("start stdio: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("start: %w", err)
	}
	tools, err := initAndList(ctx, name, c)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return tools, c, nil
}

func loadHTTP(ctx context.Context, name string, s ServerDef, httpClient *http.Client) ([]local.Tool, *mcpclient.Client, error) {
	headers, err := ExpandHeaders(name, s.Headers)
	if err != nil {
		return nil, nil, err
	}
	opts := []mcptransport.StreamableHTTPCOption{}
	if httpClient != nil {
		opts = append(opts, mcptransport.WithHTTPBasicClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, mcptransport.WithHTTPHeaders(headers))
	}
	c, err := mcpclient.NewStreamableHttpClient(s.URL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create http client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("start: %w", err)
	}
	tools, err := initAndList(ctx, name, c)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return tools, c, nil
}

func loadSSE(ctx context.Context, name string, s ServerDef, httpClient *http.Client) ([]local.Tool, *mcpclient.Client, error) {
	headers, err := ExpandHeaders(name, s.Headers)
	if err != nil {
		return nil, nil, err
	}
	opts := []mcptransport.ClientOption{}
	if httpClient != nil {
		opts = append(opts, mcptransport.WithHTTPClient(httpClient))
	}
	if len(headers) > 0 {
		opts = append(opts, mcptransport.WithHeaders(headers))
	}
	c, err := mcpclient.NewSSEMCPClient(s.URL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create sse client: %w", err)
	}
	if err := c.Start(ctx); err != nil {
		c.Close()
		return nil, nil, fmt.Errorf("start: %w", err)
	}
	tools, err := initAndList(ctx, name, c)
	if err != nil {
		c.Close()
		return nil, nil, err
	}
	return tools, c, nil
}

// initAndList initializes the MCP client and fetches its tool list.
func initAndList(ctx context.Context, name string, c *mcpclient.Client) ([]local.Tool, error) {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "hangrix-agent",
		Version: "0.1.0",
	}

	_, err := c.Initialize(ctx, initReq)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	listReq := mcp.ListToolsRequest{}
	result, err := c.ListTools(ctx, listReq)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	tools := make([]local.Tool, 0, len(result.Tools))
	for _, t := range result.Tools {
		tools = append(tools, &mcpTool{
			name:        t.Name,
			description: t.Description,
			schema:      convertSchema(t),
			client:      c,
		})
	}
	return tools, nil
}
