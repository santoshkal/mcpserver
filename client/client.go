package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	log "github.com/sirupsen/logrus"
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

// Config holds the map of server names → URLs
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

type ServerConfig struct {
	URL     string   `json:"url,omitempty"`
	Command string   `json:"command,omitempty"`
	Env     []string `json:"env,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// MultiClient can drive tools on multiple MCP servers
type MultiClient struct {
	clients       map[string]*mcpclient.Client
	ctx           context.Context
	toolToServer  map[string]string
	serverDetails map[string]*ServerDetails
}

type ServerDetails struct {
	Tools []mcp.Tool
	Info  *mcp.InitializeResult
}

func init() {
	// Enable Trace level (everything) and show full timestamps
	log.SetLevel(log.TraceLevel)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
}

// LoadConfig reads your config.json
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading config: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("error unmarshaling config: %v", err)
	}
	return &cfg, nil
}

// NewMultiClient wires up one Client per server
func NewMultiClient(ctx context.Context, cfg *Config) (*MultiClient, error) {
	m := &MultiClient{
		clients:       make(map[string]*mcpclient.Client),
		ctx:           ctx,
		toolToServer:  make(map[string]string),
		serverDetails: make(map[string]*ServerDetails),
	}

	for name, sc := range cfg.MCPServers {
		var (
			url string
			cli *mcpclient.Client
			err error
		)

		switch {
		case sc.URL != "":
			url = sc.URL
			if !strings.HasSuffix(url, "/sse") {
				sc.URL = strings.TrimRight(url, "/") + "/sse"
			}
			cli, err = mcpclient.NewSSEMCPClient(sc.URL)
			if err != nil {
				log.Errorf("creating client error: %v", err)
				return nil, &SSEClientError{"SSE Client creation failed for " + name, err.Error()}
			}
		case sc.Command != "":
			m.serverDetails[name] = &ServerDetails{}
			cli, err = mcpclient.NewStdioMCPClient(sc.Command, sc.Env, sc.Args...)
			if err != nil {
				return nil, &SSEClientError{"STDIO Client creation failed for " + name, err.Error()}
			}
		default:
			return nil, fmt.Errorf("server '%q' must have either url or command+args", name)
		}

		// **Register notification handler here**:
		cli.OnNotification(func(n mcp.JSONRPCNotification) {
			var payload struct {
				Name   string         `json:"name"`
				Output map[string]any `json:"output"`
			}
			// raw params come in JSON format inside n.Params
			raw, _ := json.Marshal(n.Params)
			if err := json.Unmarshal(raw, &payload); err != nil {
				log.Printf("[Notification][%s] failed to decode params: %v", name, err)
				return
			}
			log.Infof("[Notification][%s] Tool '%s' result notification: %+v",
				name, payload.Name, payload.Output)
		})

		m.clients[name] = cli
		m.serverDetails[name] = &ServerDetails{}
	}
	return m, nil
}

// StartAll and InitializeAll
func (m *MultiClient) StartAll() error {
	var upServers []string
	for name, cli := range m.clients {
		if err := cli.Start(m.ctx); err != nil {
			log.Warnf("Server %q failed to start: %v; marking as down", name, err)
			delete(m.clients, name)
			continue
		}
		upServers = append(upServers, name)
	}
	if len(upServers) == 0 {
		return fmt.Errorf("no servers running")
	}
	log.Infof("Server '%v' started successfully", upServers)
	return nil
}

func (m *MultiClient) InitializeAll() error {
	var inited []string
	for name, cli := range m.clients {
		req := mcp.InitializeRequest{}
		req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		req.Params.ClientInfo = mcp.Implementation{
			Name:    "multi-mcp-client",
			Version: "1.0.0",
		}
		res, err := cli.Initialize(m.ctx, req)
		if err != nil {
			log.Warnf("Initialization failed for %q: %v; dropping", name, err)
			delete(m.clients, name)
			continue
		}
		m.serverDetails[name].Info = res
		inited = append(inited, name)
	}
	if len(inited) == 0 {
		return &SSEClientStartError{fmt.Sprintf("No servers found, Initialization failed: %v", len(inited))}
	}
	log.Infof("Initialized Server: %v", inited)
	return nil
}

// ListAllToolsRaw populates toolToServer
func (m *MultiClient) ListAllToolsRaw() (map[string][]mcp.Tool, error) {
	all := make(map[string][]mcp.Tool)
	for name, cli := range m.clients {
		res, err := cli.ListTools(m.ctx, mcp.ListToolsRequest{})
		if err != nil {
			return nil, &SSEClientError{"ListTools for " + name, err.Error()}
		}
		all[name] = res.Tools
		m.serverDetails[name].Tools = res.Tools
		for _, t := range res.Tools {
			m.toolToServer[t.Name] = name
		}
	}
	return all, nil
}

func (m *MultiClient) ListToolsJSON() (string, error) {
	all, err := m.ListAllToolsRaw()
	if err != nil {
		log.Printf("error listing tools: %v", err)
		return "", err
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return "", &SSEClientError{"Marshal tools", err.Error()}
	}
	return string(b), nil
}

// CallTool dispatches the right MCPClient.CallTool
func (m *MultiClient) CallTool(tool string, args map[string]any) (string, error) {
	ts := strings.Split(tool, ".")
	srv, ok := m.toolToServer[ts[1]]
	if !ok {
		return "", &SSEClientError{"CallTool", "no server for tool " + ts[1]}
	}
	cli := m.clients[srv]

	// Build and send the CallToolRequest
	req := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: "tools/call",
		},
	}
	req.Params.Name = ts[1]
	req.Params.Arguments = args

	res, err := cli.CallTool(m.ctx, req)
	if err != nil {
		return "", &SSEClientError{"CallTool", err.Error()}
	}

	// Start assembling a detailed report
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Tool '%s' completed.\n", ts[1]))
	b.WriteString(fmt.Sprintf("  IsError: %v\n", res.IsError))

	if len(res.Content) == 0 {
		b.WriteString("  (no content returned)\n")
		return b.String(), nil
	}

	// Iterate every Content entry
	for i, c := range res.Content {
		b.WriteString(fmt.Sprintf("  Content #%d:\n", i+1))
		b.WriteString(fmt.Sprintf("  TextContent: %v\n", c))
		switch v := c.(type) {
		case *mcp.TextContent:
			if v.Type == "Text" {
				b.WriteString(fmt.Sprintf("    Text:\n%s\n", indent(v.Text, "      ")))
			}
		case *mcp.ImageContent:
			b.WriteString("    Type: image\n")
			b.WriteString(fmt.Sprintf("    MIMEType: %s\n", v.MIMEType))
			b.WriteString(fmt.Sprintf("    Data length: %d bytes (base64)\n", len(v.Data)))
		default:
			b.WriteString(fmt.Sprintf("    Unknown content type: %#v\n", v))
		}
	}

	return b.String(), nil
}

// indent prefixes each line in s with prefix (for nicer multiline formatting)
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func (m *MultiClient) Close() {
	for _, cli := range m.clients {
		cli.Close()
	}
}

// ToolCall is the LLM→JSON schema
type ToolCall struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

func main() {
	var (
		cfgPath  = flag.String("config", "", "Path to config.json")
		baseURL  = flag.String("baseurl", "http://localhost:1234/sse", "Single SSE URL")
		toolName = flag.String("tool", "", "Name of the tool to call")
		argsJSON = flag.String("arguments", "{}", "JSON string of the tool's arguments")
	)
	flag.Parse()

	if *toolName == "" {
		log.Fatal("Please supply -tool")
	}

	// Parse the arguments JSON into a map
	var userArgs map[string]any
	if err := json.Unmarshal([]byte(*argsJSON), &userArgs); err != nil {
		log.Fatalf("Failed to parse -arguments JSON: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Initialize MCP client(s)
	var cli *MultiClient
	var err error
	if *cfgPath != "" {
		cfg, e := LoadConfig(*cfgPath)
		if e != nil {
			log.Fatalf("LoadConfig: %v", e)
		}
		cli, err = NewMultiClient(ctx, cfg)
	} else {
		cfg := &Config{
			MCPServers: map[string]ServerConfig{
				"default": {URL: *baseURL},
			},
		}
		cli, err = NewMultiClient(ctx, cfg)
	}
	if err != nil {
		log.Fatalf("Client init: %v", err)
	}
	defer cli.Close()

	if startErr := cli.StartAll(); startErr != nil {
		log.Fatalf("StartAll: %v", startErr)
	}
	if initErr := cli.InitializeAll(); initErr != nil {
		log.Fatalf("InitializeAll: %v", initErr)
	}
	fmt.Println("[DEBUG] Initialized all servers")

	// List available tools to include in the LLM system prompt
	toolsJSON, err := cli.ListToolsJSON()
	if err != nil {
		log.Fatalf("ListTools: %v", err)
	}

	// Build the system prompt with the list of tools
	systemPrompt := fmt.Sprintf(`
You are an assistant that validates and normalizes tool calls for MCP, based on the available tools.
Here are the available tools:
%s
Respond only with JSON matching the schema:
{"tool":"<tool_name>", "arguments": {<key>: <value>, ...}}
`, toolsJSON)

	// Build the user message containing the raw tool name and arguments
	inputCall := ToolCall{
		Tool:      *toolName,
		Arguments: userArgs,
	}
	inputBytes, _ := json.Marshal(inputCall)
	userPrompt := string(inputBytes)

	history := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, userPrompt),
	}

	// Initialize LLM
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("set OPENAI_API_KEY")
	}
	llm, err := openai.New(openai.WithModel("gpt-4"), openai.WithToken(apiKey))
	if err != nil {
		log.Fatalf("OpenAI init: %v", err)
	}

	// Ask the LLM to produce a validated ToolCall JSON
	resp, err := llm.GenerateContent(ctx, history)
	if err != nil {
		log.Fatalf("LLM error: %v", err)
	}

	reply := resp.Choices[0].Content
	fmt.Printf("[DEBUG] LLM reply: %s\n", reply)

	// Strip markdown fencing if present
	clean := reply
	if parts := strings.Split(reply, "```"); len(parts) > 1 {
		clean = strings.TrimSpace(parts[1])
	}

	var tc ToolCall
	if er := json.Unmarshal([]byte(clean), &tc); er != nil {
		log.Fatalf("Parse JSON: %v", er)
	}
	fmt.Printf("[DEBUG] Parsed tool call: %+v\n", tc)

	if tc.Tool == "none" {
		fmt.Println("No tool needed.")
		return
	}

	// Dispatch the validated tool call
	result, err := cli.CallTool(tc.Tool, tc.Arguments)
	if err != nil {
		log.Fatalf("Tool call: %v", err)
	}
	fmt.Printf("Tool '%s' result:\n%s\n", tc.Tool, result)
}
