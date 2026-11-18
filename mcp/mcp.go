// Package toolmcp serves a tool's registry as MCP tools (the official go-sdk):
// one tool per command offered to MCP, named with underscores (list-cards
// is list_cards), with the command's input schema. Results are JSON (as
// structured content and text) carrying the shared state's revision and the
// changes since this session's last call; errors are tool errors carrying
// {"error": {"code", "message"}}. See opinionated/modes.md.
package toolmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gerbillinae/go-tool-layers/cli"
	"github.com/gerbillinae/go-tool-layers/tool"
)

// Options for the server.
type Options struct {
	Registry     *tool.Registry
	Instructions string
	// Shared, if the tool has shared state: every result then carries the
	// changes since the session's last call.
	Shared tool.Shared
}

// ToolName is a command's MCP name.
func ToolName(command string) string { return strings.ReplaceAll(command, "-", "_") }

// NewServer makes the MCP server.
func NewServer(o Options) *mcp.Server {
	r := o.Registry
	server := mcp.NewServer(&mcp.Implementation{Name: r.Name, Version: r.Version}, &mcp.ServerOptions{Instructions: o.Instructions})
	var rep *tool.Reporter
	if o.Shared != nil {
		rep = tool.NewReporter(o.Shared, 0)
	}
	for _, c := range r.Commands() {
		if !c.Offered(tool.MCP) {
			continue
		}
		if ok, _ := r.Available(c); !ok {
			continue
		}
		desc := c.Description
		if desc == "" {
			desc = strings.TrimSpace(c.Summary + " " + c.LongText)
		}
		server.AddTool(&mcp.Tool{Name: ToolName(c.Name), Description: tool.Render(desc, tool.MCP), InputSchema: r.Schema(c.Name)}, handler(r, c, rep))
	}
	return server
}

func handler(r *tool.Registry, c *tool.Command, rep *tool.Reporter) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var n atomic.Int64
		cx := tool.Context{By: "mcp", Reporter: rep, Progress: func(m string) {
			cli.Logf("%s", m)
			if token := req.Params.GetProgressToken(); token != nil && req.Session != nil {
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Message: m, Progress: float64(n.Add(1))})
			}
		}}
		out, err := r.Invoke(ctx, cx, c.Name, req.Params.Arguments)
		if err != nil {
			e := tool.AsError(err)
			cli.Logf("error [%s]: %s", e.Code, tool.Render(e.Message, tool.MCP))
		}
		var body map[string]any
		if c.OwnChanges {
			body = tool.Body(out, err, tool.MCP)
		} else {
			body = tool.WithChanges(rep, out, err, tool.MCP)
		}
		text, _ := json.MarshalIndent(body, "", "  ")
		res := &mcp.CallToolResult{IsError: err != nil, Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}
		if err == nil {
			res.StructuredContent = body
		}
		return res, nil
	}
}

// RunStdio serves MCP over stdio until the client disconnects. stdout then
// carries only MCP messages (cli.MCPMode), and logs go to stderr.
func RunStdio(ctx context.Context, server *mcp.Server, name string) error {
	cli.MCPMode = true
	if cli.StdinIsTerminal() {
		fmt.Fprintf(os.Stderr, "%s mcp is an MCP server for a client to start (e.g. from .mcp.json); it's waiting for MCP messages on stdin.\n", name)
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}
