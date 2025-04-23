package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	img "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolHandler defines the signature for our tool functions.
type ToolHandler func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

// jsonRPCRequest is the structure expected for incoming JSON‑RPC requests.
// We use the JSON‑RPC "method" value to populate the MCP
// CallToolRequest’s Params.Name field.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  struct {
		Arguments map[string]interface{} `json:"arguments"`
	} `json:"params"`
	ID interface{} `json:"id"`
}

// jsonRPCResponse defines the structure we return for JSON‑RPC responses.
type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

var (
	mcpServer    *server.MCPServer
	toolHandlers = map[string]ToolHandler{}
)

func main() {
	// Create and configure the MCP server.
	mcpServer = server.NewMCPServer(
		"MCP Tool STDIO Server",
		"v1.0.0",
		server.WithResourceCapabilities(true, true),
		server.WithLogging(),
		server.WithPromptCapabilities(false),
	)
	mcpServer.AddNotificationHandler("notifications/error", handleNotification)

	// --- Register the MarkitDown tool ---

	markItDownTool := mcp.NewTool("to-markdown",
		mcp.WithDescription("Converts the provided input file to Markdown"),
		mcp.WithString("input",
			mcp.Required(),
			mcp.Description("The path to the input file"),
		),
		mcp.WithString("output",
			mcp.Required(),
			mcp.Description("The path to the output file"),
		),
	)
	MarkItDownHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Validate the "input" and "output" arguments.
		input, ok := req.Params.Arguments["input"].(string)
		if !ok || input == "" {
			return mcp.NewToolResultText("invalid or missing input parameter"), nil
		}
		output, ok := req.Params.Arguments["output"].(string)
		if !ok || output == "" {
			return mcp.NewToolResultText("invalid or missing output parameter"), nil
		}
		// TODO: Implememt the MarkitDown CLI Command using exec.Command() to run the tool
		cmd := exec.Command("markitdown", input, "-o", output)
		outBytes, err := cmd.CombinedOutput()
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("failed to run markitdown: %v\nOutput: %s", err, string(outBytes))), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Conversion successful. Output:\n%s", output)), nil
	}

	mcpServer.AddTool(markItDownTool, MarkItDownHandler)
	toolHandlers["to-markdown"] = MarkItDownHandler

	// Register ast-grep tool
	searchCodeTool := mcp.NewTool("ast-grep",
		mcp.WithDescription("Search for code in a file"),
		mcp.WithString("pattern",
			mcp.Required(),
			mcp.Description("The pattern to search for"),
		),
		mcp.WithString("new-pattern",
			mcp.Required(),
			mcp.Description("The pattern to replace with"),
		),
		mcp.WithString("language",
			mcp.Required(),
			mcp.Description("The language to search in"),
		),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("The path to the file/directory to search in"),
		),
	)
	searchCodeHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// 1. Validate
		pattern, ok := req.Params.Arguments["pattern"].(string)
		if !ok || pattern == "" {
			return mcp.NewToolResultText("invalid or missing 'pattern' parameter"), nil
		}
		newPattern, ok := req.Params.Arguments["new-pattern"].(string)
		if !ok || newPattern == "" {
			return mcp.NewToolResultText("invalid or missing 'new-pattern' parameter"), nil
		}
		lang, ok := req.Params.Arguments["language"].(string)
		if !ok || lang == "" {
			return mcp.NewToolResultText("invalid or missing 'language' parameter"), nil
		}
		pathParam, ok := req.Params.Arguments["path"].(string)
		if !ok || pathParam == "" {
			return mcp.NewToolResultText("invalid or missing 'path' parameter"), nil
		}

		// 2. Split paths (comma or space)
		var paths []string
		if strings.Contains(pathParam, ",") {
			for _, p := range strings.Split(pathParam, ",") {
				if t := strings.TrimSpace(p); t != "" {
					paths = append(paths, t)
				}
			}
		} else {
			paths = strings.Fields(pathParam)
		}

		// 3. Build CLI args
		args := []string{
			"--pattern", pattern,
			"--rewrite", newPattern,
			"--lang", lang,
			"-U",
		}
		args = append(args, paths...)

		//  Run ast-grep
		outBytes, err := exec.Command("ast-grep", args...).CombinedOutput()
		out := strings.TrimSpace(string(outBytes))

		// If the CLI itself errored *and* produced no output, treat as “no matches”
		if err != nil && out == "" {
			msg := fmt.Sprintf("No occurrences of '%s' found in %v", pattern, paths)
			return mcp.NewToolResultText(msg), nil
		}
		// If the CLI errored *with* some output, return that as the text
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("ast-grep error: %v\n\n%s", err, out)), nil
		}
		// If CLI succeeded but no matches, still say so
		if out == "" {
			msg := fmt.Sprintf("No occurrences of '%s' found in %v", pattern, paths)
			return mcp.NewToolResultText(msg), nil
		}
		// Otherwise return the real diff/matches
		return mcp.NewToolResultText(out), nil
	}

	mcpServer.AddTool(searchCodeTool, searchCodeHandler)
	toolHandlers["ast-grep"] = searchCodeHandler

	// Add Mirrord tool
	mirrordTool := mcp.NewTool("mirrord-exec",
		mcp.WithDescription("Run `mirrord exec` using a given config file"),
		mcp.WithString("config",
			mcp.Required(),
			mcp.Description("Path to the mirrord JSON config file"),
		),
	)

	mirrordHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Validate config param
		cfg, ok := req.Params.Arguments["config"].(string)
		if !ok || cfg == "" {
			return mcp.NewToolResultText("invalid or missing 'config' parameter"), nil
		}

		// Build and run: mirrord exec --config=<cfg>
		cmd := exec.Command("mirrord", "exec", "--config="+cfg)
		out, err := cmd.CombinedOutput()
		text := string(out)

		if err != nil {
			return mcp.NewToolResultText(
				fmt.Sprintf("mirrord exec failed: %v\n\n%s", err, text),
			), nil
		}
		return mcp.NewToolResultText(text), nil
	}

	mcpServer.AddTool(mirrordTool, mirrordHandler)
	toolHandlers["mirrord-exec"] = mirrordHandler

	// --- Register the pull_image tool ---
	PullImageTool := mcp.NewTool("pull_image",
		mcp.WithDescription("Pull an image from Docker Hub"),
		mcp.WithString("image",
			mcp.Description("Name of the Docker image to pull (e.g., 'nginx:latest')"),
		),
	)
	PullImageHandler := func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Validate the "image" argument.
		image, ok := req.Params.Arguments["image"].(string)
		if !ok || image == "" {
			return mcp.NewToolResultText("invalid or missing image parameter"), nil
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] Invoking tool 'pull_image' with image: %s\n", image)

		// Use the Docker client to pull the image.
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return nil, fmt.Errorf("failed to create Docker client: %v", err)
		}
		out, err := cli.ImagePull(ctx, image, img.PullOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to pull image: %v", err)
		}
		defer out.Close()
		// Log the Docker pull progress.
		_, err = io.Copy(os.Stderr, out)
		if err != nil {
			return nil, fmt.Errorf("error reading Docker pull response: %v", err)
		}
		return mcp.NewToolResultText(fmt.Sprintf("Image '%s' pulled successfully", image)), nil
	}
	mcpServer.AddTool(PullImageTool, PullImageHandler)
	toolHandlers["pull_image"] = PullImageHandler

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
	mcpServer.AddTool(getPodsTool, getPodsHandler)
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
		if !ok || directory == "" {
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
	mcpServer.AddTool(gitInitTool, gitInitHandler)
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
		if !ok || tableName == "" {
			return nil, fmt.Errorf("invalid or missing table_name parameter")
		}
		headers, ok := req.Params.Arguments["headers"].(string)
		if !ok || headers == "" {
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
	mcpServer.AddTool(createTableTool, createTableHandler)
	toolHandlers["create_table"] = createTableHandler

	// Start HTTP JSON‑RPC endpoint on /rpc.
	http.HandleFunc("/rpc", rpcHandler)
	go func() {
		fmt.Println("HTTP JSON‑RPC server started on port 1234")
		if err := http.ListenAndServe(":1234", nil); err != nil {
			fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
			os.Exit(1)
		}
	}()

	// Optionally, start the SSE server on another port (1235).
	sseServer := server.NewSSEServer(mcpServer)
	if err := sseServer.Start(":1235"); err != nil {
		fmt.Fprintf(os.Stderr, "SSE server error: %v\n", err)
	}

	// Block forever.
	select {}
}

func rpcHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST supported", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Cannot read request body", http.StatusBadRequest)
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Look up the appropriate tool handler.
	handler, ok := toolHandlers[req.Method]
	if !ok {
		http.Error(w, "Unknown method", http.StatusNotFound)
		return
	}

	// Create an MCP CallToolRequest, mapping the JSON‑RPC "method"
	// to the Params.Name field and the provided arguments accordingly.
	var toolReq mcp.CallToolRequest
	toolReq.Params.Name = req.Method
	toolReq.Params.Arguments = req.Params.Arguments

	// Invoke the tool handler.
	result, err := handler(context.Background(), toolReq)
	var resp jsonRPCResponse
	resp.JSONRPC = "2.0"
	resp.ID = req.ID
	if err != nil {
		resp.Error = err.Error()
	} else {
		// Extract output text from the Content field.
		var output string
		if len(result.Content) > 0 {
			// Assume the text content is of type *mcp.TextContent.
			if tc, ok := result.Content[0].(*mcp.TextContent); ok {
				output = tc.Text
			} else {
				// Fallback if the type assertion doesn't match.
				output = fmt.Sprintf("%v", result.Content[0])
			}
		}
		resp.Result = output
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func handleNotification(ctx context.Context, notification mcp.JSONRPCNotification) {
	fmt.Printf("Received notification from client: %s\n", notification.Method)
}
