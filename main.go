package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"

	img "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolHandler is a function that processes an MCP call.
type ToolHandler func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)

// toolHandlers holds the mapping from tool names to their handlers.
var toolHandlers = map[string]ToolHandler{}

func main() {
	// Create a new MCP server instance.
	s := server.NewMCPServer(
		"MCP Tool Server",
		"1.0.0",
		server.WithResourceCapabilities(true, true),
		server.WithLogging(),
	)

	pullImageTool := mcp.NewTool("pull_image",
		mcp.WithDescription("Pull an image from Docker Hub"),
		mcp.WithString("image",
			mcp.Required(),
			mcp.Description("Name of the Docker image to pull (e.g., 'nginx:latest')"),
		),
	)
	pullImageHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		image, ok := request.Params.Arguments["image"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing image parameter")
		}
		fmt.Printf("[DEBUG] Executing tool 'pull_image' with image: %s\n", image)
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker client: %v", err)
		}
		out, err := cli.ImagePull(ctx, image, img.PullOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to pull image: %v", err)
		}
		defer out.Close()
		_, err = io.Copy(os.Stdout, out)
		if err != nil {
			return nil, fmt.Errorf("error reading Docker pull response: %v", err)
		}
		return mcp.NewToolResultText(fmt.Sprintf("Image '%s' pulled successfully", image)), nil
	}
	s.AddTool(pullImageTool, pullImageHandler)
	toolHandlers["pull_image"] = pullImageHandler

	getPodsTool := mcp.NewTool("get_pods",
		mcp.WithDescription("Get Kubernetes Pods from the cluster"),
	)
	getPodsHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fmt.Println("[DEBUG] Executing tool 'get_pods'")
		cmd := exec.Command("kubectl", "get", "pods")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("failed to get pods: %v, output: %s", err, string(output))
		}
		return mcp.NewToolResultText(string(output)), nil
	}
	s.AddTool(getPodsTool, getPodsHandler)
	toolHandlers["get_pods"] = getPodsHandler

	// --- Register the git_init tool ---
	gitInitTool := mcp.NewTool("git_init",
		mcp.WithDescription("Initialize a Git repository in the provided project directory"),
		mcp.WithString("directory",
			mcp.Required(),
			mcp.Description("Path to the project directory"),
		),
	)
	gitInitHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		directory, ok := request.Params.Arguments["directory"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing directory parameter")
		}
		fmt.Printf("[DEBUG] Executing tool 'git_init' with directory: %s\n", directory)
		cmd := exec.Command("git", "init", directory)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize git repository: %v, output: %s", err, string(output))
		}
		return mcp.NewToolResultText(string(output)), nil
	}
	s.AddTool(gitInitTool, gitInitHandler)
	toolHandlers["git_init"] = gitInitHandler

	// --- Register the create_table tool ---
	createTableTool := mcp.NewTool("create_table",
		mcp.WithDescription("Create a database table in a local Postgres DB instance"),
		mcp.WithString("table_name",
			mcp.Required(),
			mcp.Description("Name of the table to create"),
		),
		mcp.WithString("headers",
			mcp.Required(),
			mcp.Description("Comma separated column definitions (e.g., 'id SERIAL PRIMARY KEY, name TEXT')"),
		),
		mcp.WithString("values",
			mcp.Required(),
			mcp.Description("Comma separated list of values to insert (e.g., '1, \"John\"')"),
		),
	)
	createTableHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tableName, ok := request.Params.Arguments["table_name"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing table_name parameter")
		}
		headers, ok := request.Params.Arguments["headers"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing headers parameter")
		}
		values, ok := request.Params.Arguments["values"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing values parameter")
		}
		fmt.Printf("[DEBUG] Executing tool 'create_table' with table_name: %s\n", tableName)
		sqlCmd := fmt.Sprintf("CREATE TABLE %s (%s); INSERT INTO %s VALUES (%s);", tableName, headers, tableName, values)
		cmd := exec.Command("psql", "-d", "postgres", "-c", sqlCmd)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("failed to create table: %v, output: %s", err, string(output))
		}
		return mcp.NewToolResultText(string(output)), nil
	}
	s.AddTool(createTableTool, createTableHandler)
	toolHandlers["create_table"] = createTableHandler
	http.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		// Read the request body for debugging
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Log the raw request JSON
		fmt.Printf("[DEBUG] Received Request: %s\n", string(body))
		// Reset the body for further decoding
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		var req mcp.CallToolRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "failed to decode request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Retrieve the tool name from req.Params.Name
		toolName := req.Params.Name
		fmt.Printf("[DEBUG] Tool invoked: %s\n", toolName)
		handler, exists := toolHandlers[toolName]
		if !exists {
			http.Error(w, "unknown tool: "+toolName, http.StatusBadRequest)
			return
		}

		// Execute the tool handler.
		result, err := handler(r.Context(), req)
		if err != nil {
			http.Error(w, "tool error: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Encode the result to JSON for logging and response.
		respBody, err := json.Marshal(result)
		if err != nil {
			http.Error(w, "failed to encode response: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Log the response body.
		fmt.Printf("[DEBUG] Response: %s\n", string(respBody))

		// Return the result as JSON.
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBody)
	})

	fmt.Println("MCP HTTP Server listening on http://localhost:1234/rpc")
	if err := http.ListenAndServe(":1234", nil); err != nil {
		fmt.Printf("Server error: %v\n", err)
	}
}

// 	http.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
// 		var req mcp.CallToolRequest
// 		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 			http.Error(w, "failed to decode request: "+err.Error(), http.StatusBadRequest)
// 			return
// 		}
//
// 		// Retrieve the tool name from req.Params.Name
// 		toolName := req.Params.Name
// 		handler, exists := toolHandlers[toolName]
// 		if !exists {
// 			http.Error(w, "unknown tool: "+toolName, http.StatusBadRequest)
// 			return
// 		}
//
// 		result, err := handler(r.Context(), req)
// 		if err != nil {
// 			http.Error(w, "tool error: "+err.Error(), http.StatusInternalServerError)
// 			return
// 		}
//
// 		// Return the result as JSON.
// 		w.Header().Set("Content-Type", "application/json")
// 		if err := json.NewEncoder(w).Encode(result); err != nil {
// 			http.Error(w, "failed to encode response: "+err.Error(), http.StatusInternalServerError)
// 		}
// 	})
//
// 	fmt.Println("MCP HTTP Server listening on http://localhost:1234/rpc")
// 	if err := http.ListenAndServe(":1234", nil); err != nil {
// 		fmt.Printf("Server error: %v\n", err)
// 	}
// }
