# MCP Server with multiple Tools and a sigle client to interact with it

## To build the Client and Server executables
- Build the client:
`go build -o /mcpclient ./client`
- Build the MCP Server:
`go build -o ./mcpserver ./server`

## Start the MCP Server:
`./mcpserver`

This will start the MCP server on localhost port :1234

## Now run the client with following commands:

```sh
./mcpclient -baseurl http://localhost:1234/sse -prompt "Pull a latest redis image"
```

You should see the logs in the following format:
```sh
./mcpclient -baseurl http://localhost:1234/sse -prompt "Pull a latest redis image"
[DEBUG] Initialized: &{Result:{Meta:map[]} ProtocolVersion:2024-11-05 Capabilities:{Experimental:map[] Logging:0xa0f8e0 Prompts:0xc0000127ba Resources:0xc0000127bc Tools:0xc0000127be} ServerInfo:{Name:MCP Tool STDIO Server Version:v1.0.0} Instructions:}
[DEBUG] LLM reply: {
  "tool": "pull_image",
  "arguments": {
    "image": "redis:latest"
  }
}
[DEBUG] Parsed tool call: {Tool:pull_image Arguments:map[image:redis:latest]}
Tool 'pull_image' result:
"
```

Currently this implementation contains lots of debug logs which can be cleaned up of this code is
used in future.

