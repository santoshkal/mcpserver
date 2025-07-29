# MCP Server with multiple Tools and support a single client to interact with all the Servers

## To build the Client and Server executables

- Build the client:
  `go build -o /mcpclient ./client`
- Build the MCP Server:
  `go build -o ./mcpserver ./server`

Similarly buld and start all the servers.

## Start the MCP Server:

`./mcpserver1`
`./mcpserver2`

This will start both the MCP servers one on localhost on their respective Ports

## Create the config JSON file with all the server details.

This config will be read by the client to decide which server the tool belongs and make TooCall

```json
{
  "mcpServers": {
    "server1": {
      "url": "http://localhost:1234/sse"
    },
    "mcpServers": {
      "server2": {
        "command": "uvx",
        "args": ["mcp-server-git"]
      }
    }
  }
}
```

## Now run the client with following commands:

```sh
./mcpclient -config $HOME/mcpserver/config.json \
  -tool someToolName \
  -arguments='{"param1":"value1","param2":42}'  # example
```

You should see the logs in the following format:

```sh
➜  mcpserver git:(multi-client) ✗ ./mcpclient -config /home/santosh/mcpserver/config.json -prompt "Pull a nginx latest image"
[DEBUG] Initialized all servers
[DEBUG] LLM reply: {"tool":"pull_image", "arguments":{"image":"nginx:latest"}}
[DEBUG] Parsed tool call: {Tool:pull_image Arguments:map[image:nginx:latest]}
Tool 'pull_image' result:
Tool 'pull_image' completed.
  IsError: false
  Content #1:
  TextContent: {{<nil>} text Image 'nginx:latest' pulled successfully}
    Unknown content type: mcp.TextContent{Annotated:mcp.Annotated{Annotations:(*mcp.Annotations)(nil)}, Type:"text", Text:"Image 'nginx:latest' pulled successfully"}
```

Currently this implementation contains lots of debug logs which can be cleaned up of this code is
used in future.
