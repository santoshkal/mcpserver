package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

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

// Tool is our simplified representation of an MCP tool.
type Tool struct {
	Name        string
	Description string
	// (Additional fields like InputSchema can be added here.)
}

// ConvertMCPTools converts a slice of mcp.Tool (from the SDK) into our Tool slice.
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

// Client wraps the mcp.SSEMCPClient.
type Client struct {
	mcpClient *client.SSEMCPClient
	BaseURL   string
	ctx       context.Context
}

// NewClient creates a new MCP client. (Pass the full SSE endpoint URL,
// e.g. "http://localhost:1234/sse")
func NewClient(ctx context.Context, baseURL string) (Client, error) {
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

// Start begins the SSE connection.
func (c *Client) Start() error {
	if err := c.mcpClient.Start(c.ctx); err != nil {
		return &SSEClientStartError{Message: fmt.Sprintf("Failed to start client: %v", err)}
	}
	return nil
}

// Initialize sends an initialization request to the MCP server.
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

// ListTools retrieves available tools from the MCP server.
func (c *Client) ListTools() ([]Tool, error) {
	var toolsRequest mcp.ListToolsRequest
	mcpTools, err := c.mcpClient.ListTools(c.ctx, toolsRequest)
	if err != nil {
		return nil, &SSEGetToolsError{Message: fmt.Sprintf("Failed to list tools: %v", err)}
	}
	return ConvertMCPTools(mcpTools.Tools), nil
}

// CallToolResult represents the output of a tool invocation.
type CallToolResult struct {
	Text string
	Type string
}

// CallTool invokes a tool with the given name and arguments.
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
	return CallToolResult{Text: text, Type: "text"}, nil
}

// Close shuts down the MCP client connection.
func (c *Client) Close() error {
	return c.mcpClient.Close()
}

// getTextFromResult extracts the text from an mcp.CallToolResult.
// First a direct type assertion is attempted; if that fails, it marshals the content to JSON and extracts the "text" field.
func getTextFromResult(mcpResult *mcp.CallToolResult) (string, error) {
	if len(mcpResult.Content) == 0 {
		return "", &SSEResultExtractionError{Message: "content is empty"}
	}
	if tc, ok := mcpResult.Content[0].(*mcp.TextContent); ok {
		return tc.Text, nil
	}
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

// ToolCall defines the JSON structure expected from the LLM for a tool call.
type ToolCall struct {
	Tool      string                 `json:"tool"`
	Arguments map[string]interface{} `json:"arguments"`
}

// convertToLLMTools converts our MCP Tools to langchaingo's llms.Tool format.
func convertToLLMTools(tools []Tool) []llms.Tool {
	var llmTools []llms.Tool
	for _, t := range tools {
		var parameters map[string]any
		switch t.Name {
		case "pull_image":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"image": map[string]any{
						"type":        "string",
						"description": "The Docker image to pull (e.g., 'nginx:latest')",
					},
				},
				"required": []string{"image"},
			}
		case "get_pods":
			parameters = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		case "git_init":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"directory": map[string]any{
						"type":        "string",
						"description": "The directory to initialize git",
					},
				},
				"required": []string{"directory"},
			}
		case "ast-grep":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "The pattern to use for the AST search",
					},
					"new-pattern": map[string]any{
						"type":        "string",
						"description": "The new-pattern to replace the original pattern",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "The path to the file/diretcory to search",
					},
					"language": map[string]any{
						"type":        "string",
						"description": "The programming language to use for the AST search and replace",
					},
				},
				"required": []string{"pattern", "new-pattern", "path"},
			}
		case "create_table":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"table_name": map[string]any{
						"type":        "string",
						"description": "The name of the table to create",
					},
					"values": map[string]any{
						"type":        "string",
						"description": "Comma-separated list of values to insert",
					},
					"headers": map[string]any{
						"type":        "string",
						"description": "Comma-separated column definitions",
					},
				},
				"required": []string{"table_name", "headers"},
			}
		case "to-markdown":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{
						"type":        "string",
						"description": "The path to the input file",
					},
					"output": map[string]any{
						"type":        "string",
						"description": "The path to the output file",
					},
				},
				"required": []string{"input", "output"},
			}
		case "read-query":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"db": map[string]any{
						"type":        "string",
						"description": "path to the .db file",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "The SELECT query to run",
					},
				},
			}

		case "write-query":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"db": map[string]any{
						"type":        "string",
						"description": "path to the .db file",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "The NON-SELECT query to run",
					},
				},
			}
		case "list-tables":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"db": map[string]any{
						"type":        "string",
						"description": "path to the .db file",
					},
				},
			}
		case "create-SQLtable":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"db": map[string]any{
						"type":        "string",
						"description": "Path to the .db file",
					},
					"definition": map[string]any{
						"type":        "string",
						"description": "SQL CREATE TABLE statement",
					},
				},
				"required": []string{"db", "definition"},
			}
		case "mirrord-exec":
			parameters = map[string]any{
				"type": "object",
				"properties": map[string]any{
					"config": map[string]any{
						"type":        "string",
						"description": "Path to the mirrord config file",
					},
				},
			}
		default:
			parameters = map[string]any{
				"type": "object",
			}
		}
		llmTool := llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				// Instruct the model to return the function name exactly as defined here (e.g., "pull_image").
				Name:        t.Name,
				Description: t.Description,
				Parameters:  parameters,
			},
		}
		llmTools = append(llmTools, llmTool)
	}
	return llmTools
}

// updateMessageHistory appends the assistant's response (including any tool call parts) to the history.
func updateMessageHistory(history []llms.MessageContent, resp *llms.ContentResponse) []llms.MessageContent {
	assistantResponse := llms.TextParts(llms.ChatMessageTypeAI, resp.Choices[0].Content)
	for _, tc := range resp.Choices[0].ToolCalls {
		assistantResponse.Parts = append(assistantResponse.Parts, tc)
	}
	return append(history, assistantResponse)
}

// executeToolCalls iterates over tool calls from the LLM response.
// If ToolCalls array is empty, it attempts to parse resp.Choices[0].Content as JSON to extract tool and arguments.
func executeToolCalls(ctx context.Context, mcpClient *Client, history []llms.MessageContent, resp *llms.ContentResponse) []llms.MessageContent {
	if len(resp.Choices[0].ToolCalls) == 0 {
		fmt.Println("No ToolCalls found in the response; parsing content for tool call details.")
		var toolCall ToolCall
		if err := json.Unmarshal([]byte(resp.Choices[0].Content), &toolCall); err != nil {
			log.Fatalf("Error parsing content for tool call: %v", err)
		}
		if strings.ToLower(toolCall.Tool) == "none" {
			fmt.Println("LLM determined no tool should be called.")
			return history
		}
		// Strip any "functions." prefix, if present.
		if strings.HasPrefix(toolCall.Tool, "functions.") {
			toolCall.Tool = strings.TrimPrefix(toolCall.Tool, "functions.")
		}
		fmt.Printf("Parsed tool call from content: %s\n", toolCall.Tool)
		toolResult, err := mcpClient.CallTool(toolCall.Tool, toolCall.Arguments)
		if err != nil {
			log.Fatalf("Error calling tool %s: %v", toolCall.Tool, err)
		}
		fmt.Printf("Tool %s returned: %s\n", toolCall.Tool, toolResult.Text)
		// Append the tool response to history.
		toolResponseMsg := llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{
				llms.ToolCallResponse{
					ToolCallID: "", // No ID parsed
					Name:       toolCall.Tool,
					Content:    toolResult.Text,
				},
			},
		}
		history = append(history, toolResponseMsg)
		return history
	}

	// Otherwise, iterate over each tool call in the array.
	for i := range resp.Choices[0].ToolCalls {
		tc := &resp.Choices[0].ToolCalls[i]
		if strings.HasPrefix(tc.FunctionCall.Name, "functions.") {
			tc.FunctionCall.Name = strings.TrimPrefix(tc.FunctionCall.Name, "functions.")
		}
		toolName := tc.FunctionCall.Name
		fmt.Printf("LLM requested tool call: %s\n", toolName)
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args); err != nil {
			log.Fatalf("Error parsing tool call arguments: %v", err)
		}
		toolResult, err := mcpClient.CallTool(toolName, args)
		if err != nil {
			log.Fatalf("Error calling tool %s: %v", toolName, err)
		}
		fmt.Printf("Tool %s returned: %s\n", toolName, toolResult.Text)
		toolResponseMsg := llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{
				llms.ToolCallResponse{
					ToolCallID: tc.ID,
					Name:       toolName,
					Content:    toolResult.Text,
				},
			},
		}
		history = append(history, toolResponseMsg)
	}
	return history
}

func main() {
	// Flags for MCP server SSE endpoint and user prompt.
	baseURL := flag.String("baseurl", "http://localhost:1234/sse", "Base URL for the MCP server SSE endpoint")
	userPrompt := flag.String("prompt", "", "Natural language prompt for tool invocation")
	flag.Parse()

	if *userPrompt == "" {
		log.Fatal("Please provide a prompt using -prompt")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mcpClient, err := NewClient(ctx, *baseURL)
	if err != nil {
		log.Fatalf("Error creating MCP client: %v", err)
	}
	defer mcpClient.Close()

	if err := mcpClient.Start(); err != nil {
		log.Fatalf("Error starting MCP client: %v", err)
	}

	initResult, err := mcpClient.Initialize()
	if err != nil {
		log.Fatalf("Error initializing MCP client: %v", err)
	}
	fmt.Printf("MCP Client Initialized: %+v\n", initResult)

	availableMCPTools, err := mcpClient.ListTools()
	if err != nil {
		log.Fatalf("Error listing MCP tools: %v", err)
	}
	fmt.Println("Available MCP Tools:")
	for _, t := range availableMCPTools {
		fmt.Printf(" - %s: %s\n", t.Name, t.Description)
	}
	llmTools := convertToLLMTools(availableMCPTools)

	toolListStr := ""
	for _, t := range availableMCPTools {
		toolListStr += fmt.Sprintf("- %s: %s\n", t.Name, t.Description)
	}
	systemPrompt := fmt.Sprintf(`You are a function-calling assistant.
You have access to the following functions:
%s
When deciding which function to call, return the result as JSON exactly in the following format:
{"tool": "<tool name>", "arguments": { ... } }
Do NOT prefix the tool name with any extra text (for example, do not include "functions.").
If no function is appropriate, return {"tool": "none", "arguments": {}}.`, toolListStr)

	// 4. Initialize the message history with system and user messages.
	messageHistory := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, *userPrompt),
	}

	// 5. Initialize the OpenAI LLM client with function calling support.
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("Please set the OPENAI_API_KEY environment variable")
	}
	llm, err := openai.New(openai.WithModel("gpt-4"), openai.WithToken(apiKey))
	if err != nil {
		log.Fatalf("Error creating OpenAI client: %v", err)
	}

	// 6. Generate the initial LLM response with available tools.
	resp, err := llm.GenerateContent(ctx, messageHistory, llms.WithTools(llmTools))
	if err != nil {
		log.Fatalf("Error during LLM query: %v", err)
	}
	fmt.Println("LLM initial response:")
	fmt.Println(resp.Choices[0].Content)

	messageHistory = updateMessageHistory(messageHistory, resp)

	messageHistory = executeToolCalls(ctx, &mcpClient, messageHistory, resp)

	// 9. For a final summary, append a follow-up message instructing the model to provide a summary.
	// messageHistory = append(messageHistory, llms.TextParts(llms.ChatMessageTypeHuman, "Please provide a final summary of the tool outputs in plain text, without calling any functions."))
	// finalResp, err := llm.GenerateContent(ctx, messageHistory)
	// if err != nil {
	// 	log.Fatalf("Error during follow-up LLM query: %v", err)
	// }
	// fmt.Println("Final LLM response:")
	// fmt.Println(finalResp.Choices[0].Content)
}
