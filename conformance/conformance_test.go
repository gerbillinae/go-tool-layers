package conformance

import (
	"context"
	"testing"

	"github.com/gerbillinae/go-tool-layers/rest"
	"github.com/gerbillinae/go-tool-layers/tool"
	toolmcp "github.com/gerbillinae/go-tool-layers/mcp"
)

type counter struct{ n int }

type addIn struct {
	By int `json:"by"`
}

func instance() *tool.Registry {
	c := &counter{}
	r := tool.New("t", "1", "test")
	rest.Register(r)
	r.Add(
		tool.Cmd("add", "Add.", func(_ context.Context, _ tool.Context, in addIn) (map[string]int, error) {
			c.n += in.By
			return map[string]int{"n": c.n}, nil
		}).Write(),
		tool.Cmd("get", "Get.", func(context.Context, tool.Context, struct{}) (map[string]int, error) {
			return map[string]int{"n": c.n}, nil
		}),
	)
	return r
}

func TestInterfacesAgree(t *testing.T) {
	if f := Check(instance()); len(f) > 0 {
		t.Fatal(f)
	}
	b := instance()
	via, err := Interfaces(b, toolmcp.NewServer(toolmcp.Options{Registry: b}), rest.Handler(rest.Options{Registry: b, Token: "x"}), "x")
	if err != nil {
		t.Fatal(err)
	}
	steps := []Step{
		{By: "mcp", Command: "add", Input: map[string]any{"by": 2}},
		{By: "web", Command: "add", Input: map[string]any{"by": 3}},
		{By: "web", Command: "get"},
		{By: "mcp", Command: "add", Input: map[string]any{}},
		{By: "terminal", Command: "get"},
	}
	if d := Compare(Direct(instance()), via, steps, nil); len(d) > 0 {
		t.Fatal(d)
	}
}
