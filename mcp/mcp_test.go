package toolmcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gerbillinae/go-tool-layers/tool"
)

type log struct{ rev int64 }

func (l *log) Revision() int64             { return l.rev }
func (l *log) Since(rev int64) (any, bool) { return []int64{l.rev}, false }

func TestServesTheRegistry(t *testing.T) {
	l := &log{}
	r := tool.New("t", "1", "test")
	r.Add(
		tool.Cmd("list-things", "List.", func(context.Context, tool.Context, struct{}) (map[string]int, error) {
			l.rev++
			return map[string]int{"count": 2}, nil
		}),
		tool.Cmd("cli-only", "CLI only.", func(context.Context, tool.Context, struct{}) (any, error) { return nil, nil }).Only(tool.CLI),
		tool.Cmd("fail", "Fail.", func(context.Context, tool.Context, struct{}) (any, error) {
			return nil, tool.Errorf("nope", "see {cmd:list-things}")
		}),
	)
	server := NewServer(Options{Registry: r, Shared: l})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := cs.ListTools(ctx, nil)
	if len(tools.Tools) != 2 {
		t.Fatalf("want list_things and fail, got %d", len(tools.Tools))
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_things"})
	if err != nil || res.IsError {
		t.Fatal(err, res)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body)
	if body["count"] != 2.0 || body["revision"] != 1.0 {
		t.Errorf("%v", body)
	}
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "fail"})
	_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body)
	if !res.IsError || body["error"].(map[string]any)["message"] != "see list_things" {
		t.Errorf("%v", body)
	}
}
