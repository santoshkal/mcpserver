// main.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// Custom error types.
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

type SSEResultExtractionError struct{ Message string }

func (e *SSEResultExtractionError) Error() string { return e.Message }

// Tool is a simplified type to represent a tool.
type Tool struct {
	Name        string
	Description string
}

// ConvertMCPTools converts a slice of mcp.Tool (from the SDK) into a slice of Tool.
// Adjust this based on your actual mcp.Tool fields.
func ConvertMCPTools(mcpTools []mcp.Tool) []Tool {
	var tools []Tool
	for _, t := range mcpTools {
		tools = append(tools, Tool{
			Name:        t.Name,
			Description: t.Description,
		})
	}
	return tools
}

// Client is a simple MCP client.
type Client struct {
	mcpClient *client.SSEMCPClient
	BaseURL   string
	ctx       context.Context
}

// NewClient creates a new MCP client without appending extra path parts.
// Pass the complete URL (e.g. "http://localhost:1234/rpc") as baseURL.
func NewClient(ctx context.Context, baseURL string) (Client, error) {
	// Notice: we use baseURL directly without appending "/sse"
	mcpClient, err := client.NewSSEMCPClient(baseURL)
	if err != nil {
		return Client{}, &SSEClientCreationError{Message: fmt.Sprintf("Failed to create client: %v", err)}
	}
	return Client{
		mcpClient: mcpClient,
		BaseURL:   baseURL,
		ctx:       ctx,
	}, nil
}

// Start starts the MCP client.
func (c *Client) Start() error {
	if err := c.mcpClient.Start(c.ctx); err != nil {
		return &SSEClientStartError{Message: fmt.Sprintf("Failed to start client: %v", err)}
	}
	return nil
}

// Initialize sends an InitializeRequest to the server.
func (c *Client) Initialize() (*mcp.InitializeResult, error) {
	var initRequest mcp.InitializeRequest
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "genval mcp client",
		Version: "1.0.0",
	}
	initResult, err := c.mcpClient.Initialize(c.ctx, initRequest)
	if err != nil {
		return nil, &SSEClientInitializationError{Message: fmt.Sprintf("Failed to initialize client: %v", err)}
	}
	return initResult, nil
}

// ListTools requests the list of available tools from the server.
func (c *Client) ListTools() ([]Tool, error) {
	var toolsRequest mcp.ListToolsRequest
	mcpTools, err := c.mcpClient.ListTools(c.ctx, toolsRequest)
	if err != nil {
		return nil, &SSEGetToolsError{Message: fmt.Sprintf("Failed to list tools: %v", err)}
	}
	converted := ConvertMCPTools(mcpTools.Tools)
	return converted, nil
}

// ListToolsFull returns the complete mcp.ListToolsResult for full JSON output.
func (c *Client) ListToolsFull() (*mcp.ListToolsResult, error) {
	var toolsRequest mcp.ListToolsRequest
	mcpTools, err := c.mcpClient.ListTools(c.ctx, toolsRequest)
	if err != nil {
		return nil, &SSEGetToolsError{Message: fmt.Sprintf("Failed to list tools: %v", err)}
	}
	return mcpTools, nil
}

// CallToolResult holds the output of a tool call.
type CallToolResult struct {
	Text string
	Type string
}

// CallTool calls a tool with the given name and arguments.
func (c *Client) CallTool(functionName string, arguments map[string]interface{}) (CallToolResult, error) {
	toolRequest := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: "tools/call",
		},
	}
	toolRequest.Params.Name = functionName
	toolRequest.Params.Arguments = arguments

	mcpResult, err := c.mcpClient.CallTool(c.ctx, toolRequest)
	if err != nil {
		return CallToolResult{}, &SSEToolCallError{Message: fmt.Sprintf("Failed to call tool: %v", err)}
	}

	text, err := getTextFromResult(mcpResult)
	if err != nil {
		return CallToolResult{}, &SSEToolCallError{Message: fmt.Sprintf("Failed to extract text from result: %v", err)}
	}

	// Assume the result is text.
	return CallToolResult{Text: text, Type: "text"}, nil
}

// Close closes the client connection.
func (c *Client) Close() error {
	return c.mcpClient.Close()
}

func getTextFromResult(mcpResult *mcp.CallToolResult) (string, error) {
	if len(mcpResult.Content) == 0 {
		return "", &SSEResultExtractionError{Message: "content is empty"}
	}

	// First, try to see if the content is of the expected concrete type.
	if tc, ok := mcpResult.Content[0].(*mcp.TextContent); ok {
		return tc.Text, nil
	}

	// Fallback: because the content was serialized/deserialized, it may now be a generic map.
	// We marshal the content back to JSON and unmarshal it into a map.
	fallbackJSON, err := json.Marshal(mcpResult.Content[0])
	if err != nil {
		return "", &SSEResultExtractionError{Message: fmt.Sprintf("failed to marshal content: %v", err)}
	}
	var m map[string]interface{}
	if err := json.Unmarshal(fallbackJSON, &m); err != nil {
		return "", &SSEResultExtractionError{Message: fmt.Sprintf("failed to unmarshal content: %v", err)}
	}
	if text, ok := m["text"].(string); ok {
		return text, nil
	}
	return "", &SSEResultExtractionError{Message: "unexpected content format"}
}

// Main function: processes flags to list tools or call a tool.
func main() {
	baseURL := flag.String("baseurl", "http://localhost:1234/rpc", "Base URL for the MCP server")
	command := flag.String("command", "list", "Command: list or call")
	toolName := flag.String("tool", "", "Tool name (for call command)")
	argsJSON := flag.String("args", "{}", "Arguments in JSON format (for call command)")
	fullJSON := flag.Bool("json", false, "Output full ListToolsResult JSON (only used with command=list)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := NewClient(ctx, *baseURL)
	if err != nil {
		log.Fatalf("Error creating client: %v", err)
	}
	defer client.Close()

	if err := client.Start(); err != nil {
		log.Fatalf("Error starting client: %v", err)
	}

	initResult, err := client.Initialize()
	if err != nil {
		log.Fatalf("Error initializing client: %v", err)
	}
	fmt.Printf("Initialization succeeded: %+v\n", initResult)

	switch *command {
	case "list":
		if *fullJSON {
			// Fetch the complete ListToolsResult and print its JSON.
			toolsResult, err := client.ListToolsFull()
			if err != nil {
				log.Fatalf("Error listing tools: %v", err)
			}
			jsonBytes, err := json.MarshalIndent(toolsResult, "", "  ")
			if err != nil {
				log.Fatalf("Error marshaling tools result: %v", err)
			}
			fmt.Println("Complete ListToolsResult in JSON:")
			fmt.Println(string(jsonBytes))
		} else {
			tools, err := client.ListTools()
			if err != nil {
				log.Fatalf("Error listing tools: %v", err)
			}
			fmt.Println("Available Tools:")
			for _, t := range tools {
				fmt.Printf(" - %s: %s\n", t.Name, t.Description)
			}
		}
	case "call":
		if *toolName == "" {
			log.Fatalf("Tool name must be provided for 'call' command")
		}
		var arguments map[string]interface{}
		if err := json.Unmarshal([]byte(*argsJSON), &arguments); err != nil {
			log.Fatalf("Error parsing args JSON: %v", err)
		}
		result, err := client.CallTool(*toolName, arguments)
		if err != nil {
			log.Fatalf("Error calling tool: %v", err)
		}
		fmt.Printf("CallTool result: %s\n", result.Text)
	default:
		log.Fatalf("Unknown command: %s", *command)
	}
}
