package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	img "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Define our ToolHandler type for clarity.
type ToolHandler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

func main() {
	// Create a new MCP server instance.
	s := server.NewMCPServer(
		"MCP Tool STDIO Server",
		"1.0.0",
		server.WithResourceCapabilities(true, true),
		server.WithLogging(),
	)

	// We'll also use a mapping from tool name to handler, so we can
	// manually dispatch calls in our custom STDIO loop.
	toolHandlers := make(map[string]ToolHandler)

	// --- Register the pull_image tool ---
	pullImageTool := mcp.NewTool("pull_image",
		mcp.WithDescription("Pull an image from Docker Hub"),
		mcp.WithString("image",
			mcp.Description("Name of the Docker image to pull (e.g., 'nginx:latest')"),
		),
	)
	pullImageHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		image, ok := req.Params.Arguments["image"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing image parameter")
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] Invoking tool 'pull_image' with image: %s\n", image)
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker client: %v", err)
		}
		out, err := cli.ImagePull(ctx, image, img.PullOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to pull image: %v", err)
		}
		defer out.Close()
		// Log Docker pull progress.
		_, err = io.Copy(os.Stderr, out)
		if err != nil {
			return nil, fmt.Errorf("error reading Docker pull response: %v", err)
		}
		return mcp.NewToolResultText(fmt.Sprintf("Image '%s' pulled successfully", image)), nil
	}
	s.AddTool(pullImageTool, pullImageHandler)
	toolHandlers["pull_image"] = pullImageHandler

	// --- Register the get_pods tool ---
	getPodsTool := mcp.NewTool("get_pods",
		mcp.WithDescription("Get Kubernetes Pods from the cluster"),
	)
	getPodsHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fmt.Fprintln(os.Stderr, "[DEBUG] Invoking tool 'get_pods'")
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
			mcp.Description("Path to the project directory"),
		),
	)
	gitInitHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		directory, ok := req.Params.Arguments["directory"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing directory parameter")
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] Invoking tool 'git_init' with directory: %s\n", directory)
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
			mcp.Description("Name of the table to create"),
		),
		mcp.WithString("headers",
			mcp.Required(),
			mcp.Description("Comma separated column definitions (e.g., 'id SERIAL PRIMARY KEY, name TEXT')"),
		),
		mcp.WithString("values",
			mcp.Description("Comma separated list of values to insert (e.g., '1, \"John\"')"),
		),
	)
	createTableHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tableName, ok := req.Params.Arguments["table_name"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing table_name parameter")
		}
		headers, ok := req.Params.Arguments["headers"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing headers parameter")
		}
		values, ok := req.Params.Arguments["values"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing values parameter")
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] Invoking tool 'create_table' with table: %s\n", tableName)
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

	// Begin our custom STDIO loop.
	fmt.Fprintln(os.Stderr, "[DEBUG] Starting custom MCP STDIO server")
	reader := bufio.NewReader(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		// Read one line (one JSON object) from STDIN.
		rawInput, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				fmt.Fprintln(os.Stderr, "[DEBUG] End of STDIN stream")
				break
			}
			fmt.Fprintf(os.Stderr, "[ERROR] Reading STDIN: %v\n", err)
			continue
		}
		// Print the raw input.
		fmt.Fprintf(os.Stderr, "[DEBUG] Raw input: %s\n", string(rawInput))

		// Unmarshal the JSON input into CallToolRequest.
		var req mcp.CallToolRequest
		if err := json.Unmarshal(rawInput, &req); err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] Failed to decode JSON: %v\n", err)
			continue
		}

		// Debug-print the decoded request.
		reqDump, _ := json.MarshalIndent(req, "", "  ")
		fmt.Fprintf(os.Stderr, "[DEBUG] Decoded request:\n%s\n", reqDump)
		fmt.Fprintf(os.Stderr, "[DEBUG] Fields: top-level Method='%s', Params.Name='%s', Params.Arguments=%+v\n",
			req.Method, req.Params.Name, req.Params.Arguments)

		// If Params.Name is empty, fall back to the top-level Method.
		if req.Params.Name == "" && req.Method != "" {
			fmt.Fprintf(os.Stderr, "[DEBUG] Falling back: setting Params.Name from top-level Method '%s'\n", req.Method)
			req.Params.Name = req.Method
		}

		// Look up the tool handler by name.
		handler, found := toolHandlers[req.Params.Name]
		if !found {
			errMsg := fmt.Sprintf("Method '%s' not found", req.Params.Name)
			fmt.Fprintf(os.Stderr, "[ERROR] %s\n", errMsg)
			encoder.Encode(mcp.NewToolResultText(errMsg))
			continue
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] Found handler for method '%s'\n", req.Params.Name)

		// Invoke the handler.
		result, err := handler(context.Background(), req)
		if err != nil {
			errText := fmt.Sprintf("Error executing tool: %v", err)
			fmt.Fprintf(os.Stderr, "[ERROR] %s\n", errText)
			encoder.Encode(mcp.NewToolResultText(errText))
			continue
		}

		// Debug-print the result.
		resultDump, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintf(os.Stderr, "[DEBUG] Response:\n%s\n", resultDump)

		// Write the JSON response.
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] Failed to encode response: %v\n", err)
		}
	}
}
