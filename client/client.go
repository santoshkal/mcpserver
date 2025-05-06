package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

// MCP Errors
type SSEClientCreationError struct{ Message string }

func (e *SSEClientCreationError) Error() string { return e.Message }

type SSEClientStartError struct{ Message string }

func (e *SSEClientStartError) Error() string { return e.Message }

type SSEClientInitializationError struct{ Message string }

func (e *SSEClientInitializationError) Error() string { return e.Message }

type SSEGetToolsError struct{ Message string }

func (e *SSEGetToolsError) Error() string { return e.Message }

type SSEToolCallError struct{ Message string }

func (e *SSEToolCallError) Error() string { return e.Message }

type SSEClientError struct{ Stage, Err string }

func (e *SSEClientError) Error() string { return fmt.Sprintf("%s: %s", e.Stage, e.Err) }

// --- MCP client ----------------------------------------------------

type Client struct {
	mcpClient *mcpclient.SSEMCPClient
	ctx       context.Context
}

func NewClient(ctx context.Context, baseURL string) (*Client, error) {
	c, err := mcpclient.NewSSEMCPClient(baseURL)
	if err != nil {
		return nil, &SSEClientError{"Client creation", err.Error()}
	}
	return &Client{mcpClient: c, ctx: ctx}, nil
}

func (c *Client) Start() error {
	if err := c.mcpClient.Start(c.ctx); err != nil {
		return &SSEClientError{"Client start", err.Error()}
	}
	return nil
}

func (c *Client) Initialize() (*mcp.InitializeResult, error) {
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{
		Name:    "dynamic-mcp-client",
		Version: "1.0.0",
	}
	res, err := c.mcpClient.Initialize(c.ctx, req)
	if err != nil {
		return nil, &SSEClientError{"Initialization", err.Error()}
	}
	return res, nil
}

func (c *Client) ListToolsRaw() ([]mcp.Tool, error) {
	req := mcp.ListToolsRequest{}
	res, err := c.mcpClient.ListTools(c.ctx, req)
	if err != nil {
		return nil, &SSEClientError{"ListTools RPC", err.Error()}
	}
	return res.Tools, nil
}

func (c *Client) ListToolsJSON() (string, error) {
	tools, err := c.ListToolsRaw()
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		return "", &SSEClientError{"Marshal Tools JSON", err.Error()}
	}
	return string(b), nil
}

// CallTool invokes the given tool name with arguments.
func (c *Client) CallTool(name string, args map[string]any) (string, error) {
	req := mcp.CallToolRequest{
		Request: mcp.Request{Method: "tools/call"},
	}
	req.Params.Name = name
	req.Params.Arguments = args

	res, err := c.mcpClient.CallTool(c.ctx, req)
	if err != nil {
		return "", &SSEClientError{"Tool call", err.Error()}
	}
	// extract text
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			return tc.Text, nil
		}
	}
	return "", nil
}

// --- LLM helper ------------------------------------------------------------

// ToolCall is what we expect the model to return.
type ToolCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func main() {
	// CLI flags
	baseURL := flag.String("baseurl", "http://localhost:1234/sse", "MCP SSE endpoint")
	prompt := flag.String("prompt", "", "Natural language prompt for the assistant")
	flag.Parse()

	if *prompt == "" {
		log.Fatal("Please provide -prompt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// MCP client
	cli, err := NewClient(ctx, *baseURL)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer cli.mcpClient.Close()

	if err := cli.Start(); err != nil {
		log.Fatalf("Error: %v", err)
	}

	initRes, err := cli.Initialize()
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	fmt.Printf("[DEBUG] Initialized: %+v\n", initRes)

	// Fetch tool JSON
	toolsJSON, err := cli.ListToolsJSON()
	if err != nil {
		log.Fatalf("Error fetching tools: %v", err)
	}
	// fmt.Printf("[DEBUG] Available tools JSON:\n%s\n", toolsJSON)

	// Build system prompt
	systemPrompt := fmt.Sprintf(`
You are an assistant that invokes tools via MCP.
Here are the available tools (with their JSON schema):
%s

When you decide which tool to call in response to the user, reply *only* with a JSON object:
{
  "tool": "<tool name>",
  "arguments": { ... }
}
If you do not need to call any tool, return:
{"tool":"none","arguments":{}}
`, toolsJSON)

	// Debug
	// fmt.Printf("[DEBUG] System prompt:\n%s\n", systemPrompt)

	// Initialize LLM
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("Please set OPENAI_API_KEY")
	}
	llm, err := openai.New(openai.WithModel("gpt-4"), openai.WithToken(apiKey))
	if err != nil {
		log.Fatalf("OpenAI init error: %v", err)
	}

	// Build history
	history := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, *prompt),
	}

	// Generate without function definitions
	resp, err := llm.GenerateContent(ctx, history)
	if err != nil {
		log.Fatalf("LLM error: %v", err)
	}
	reply := resp.Choices[0].Content
	fmt.Printf("[DEBUG] LLM reply: %s\n", reply)

	// Parse the tool call
	var tc ToolCall
	if err := json.Unmarshal([]byte(reply), &tc); err != nil {
		log.Fatalf("Failed to parse JSON from model: %v", err)
	}
	fmt.Printf("[DEBUG] Parsed tool call: %+v\n", tc)

	if tc.Tool == "none" {
		fmt.Println("No tool to call.")
		return
	}

	// MCP Client invokes the chosen tool
	result, err := cli.CallTool(tc.Tool, tc.Arguments)
	if err != nil {
		log.Fatalf("Tool invocation error: %v", err)
	}
	fmt.Printf("Tool '%s' result:\n%s\n", tc.Tool, result)
}
