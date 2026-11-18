package tool

import (
	"context"
	"encoding/json"
	"testing"
)

type options struct {
	Deck   string   `json:"deck" flag:"deck" jsonschema:"The deck."`
	Tags   []string `json:"tags,omitempty" flag:"tag"`
	Cards  []string `json:"cards,omitempty" interfaces:"mcp,rest"`
	Nested          // flattened, as encoding/json does
}

type Nested struct {
	Limit int `json:"limit,omitempty"`
}

func registry() *Registry {
	r := New("t", "1", "a test tool")
	r.Add(
		Cmd("echo", "Echo the options.", func(_ context.Context, _ Context, in options) (options, error) { return in, nil }),
		Cmd("boom", "Panic.", func(context.Context, Context, struct{}) (any, error) { panic("oops") }),
		Cmd("agent-only", "Only for MCP.", func(context.Context, Context, struct{}) (any, error) { return "ok", nil }).Only(MCP),
		Cmd("needs-audio", "Needs audio.", func(context.Context, Context, struct{}) (any, error) { return "ok", nil }).Requires("audio"),
	)
	r.Features(func() map[string]bool { return map[string]bool{"audio": false} })
	return r
}

func code(err error) string {
	if err == nil {
		return ""
	}
	return AsError(err).Code
}

func TestInvokeChecks(t *testing.T) {
	r := registry()
	ctx := context.Background()
	term, mcp := Context{By: "terminal"}, Context{By: "mcp"}
	for _, c := range []struct {
		cx    Context
		cmd   string
		input string
		want  string
	}{
		{term, "echo", `{"deck": "A", "limit": 3}`, ""},
		{term, "echo", `{}`, CodeMissingArgument},
		{term, "echo", `{"deck": "A", "nope": 1}`, CodeInvalidArgument},
		{term, "echo", `{"deck": "A", "cards": ["x"]}`, CodeInvalidArgument},
		{mcp, "echo", `{"deck": "A", "cards": ["x"]}`, ""},
		{term, "agent-only", `{}`, CodeNotAvailableHere},
		{mcp, "agent-only", `{}`, ""},
		{term, "needs-audio", `{}`, CodeFeatureUnavailable},
		{term, "boom", `{}`, CodeInternal},
		{term, "missing", `{}`, CodeUnknownCommand},
	} {
		_, err := r.Invoke(ctx, c.cx, c.cmd, json.RawMessage(c.input))
		if got := code(err); got != c.want {
			t.Errorf("%s %s %s: got %q, want %q (%v)", c.cx.By, c.cmd, c.input, got, c.want, err)
		}
	}
	out, _ := r.Invoke(ctx, term, "echo", json.RawMessage(`{"deck": "A", "limit": 3}`))
	if o := out.(options); o.Deck != "A" || o.Limit != 3 {
		t.Errorf("decoded %+v", o)
	}
}

func TestRender(t *testing.T) {
	msg := "pass {arg:create_missing}, or see {cmd:list-cards}"
	if got := Render(msg, CLI); got != "pass --create-missing, or see list-cards" {
		t.Error(got)
	}
	if got := Render(msg, MCP); got != "pass create_missing, or see list_cards" {
		t.Error(got)
	}
}

type log struct{ rev int64 }

func (l *log) Revision() int64 { return l.rev }
func (l *log) Since(rev int64) (any, bool) {
	var out []int64
	for r := rev + 1; r <= l.rev; r++ {
		out = append(out, r)
	}
	return out, false
}

func TestWithChanges(t *testing.T) {
	l := &log{}
	rep := NewReporter(l, 0)
	l.rev = 2
	b := WithChanges(rep, map[string]int{"n": 1}, nil, MCP)
	if b["revision"] != int64(2) || len(b["changes_since_last_call"].([]int64)) != 2 || b["n"] != float64(1) {
		t.Errorf("%v", b)
	}
	b = WithChanges(rep, nil, Errorf("x", "see {cmd:list-cards}"), MCP)
	if b["error"].(Error).Message != "see list_cards" || b["changes_since_last_call"] != nil && len(b["changes_since_last_call"].([]int64)) != 0 {
		t.Errorf("%v", b)
	}
}

func TestDescribe(t *testing.T) {
	d := registry().Describe()
	if len(d.Commands) != 4 || d.Commands[0].Input.Properties["deck"].Description != "The deck." {
		t.Errorf("%+v", d.Commands[0])
	}
	if d.Errors[CodeInternal].Category != Internal {
		t.Error("built-in codes")
	}
}
