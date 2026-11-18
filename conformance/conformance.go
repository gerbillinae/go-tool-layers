// Package conformance checks a tool's interfaces agree. Check holds for any
// registry; Compare runs the same steps two ways (typically: straight into
// the registry, and through the interface each step's caller uses: MCP or
// REST) and reports where the results differ. Give each side its own fresh
// instance of the tool, so both start from the same state. See
// opinionated/layering.md, "Checking the interfaces agree".
package conformance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/gerbillinae/go-tool-layers/rest"
	"github.com/gerbillinae/go-tool-layers/tool"
	toolmcp "github.com/gerbillinae/go-tool-layers/mcp"
)

// Check returns what's wrong with a registry (empty: nothing).
func Check(r *tool.Registry) []string {
	var failed []string
	d := r.Describe()
	routes := map[string]string{}
	for _, c := range r.Commands() {
		for _, e := range c.Codes {
			if _, ok := d.Errors[e]; !ok {
				failed = append(failed, fmt.Sprintf("%s: error %q isn't registered", c.Name, e))
			}
		}
		for _, f := range c.Required {
			if _, ok := d.Features[f]; !ok {
				failed = append(failed, fmt.Sprintf("%s: requires %q, which Features doesn't list", c.Name, f))
			}
		}
		if c.Offered(tool.REST) {
			rt := rest.RouteOf(c)
			key := rt.Method + " " + rt.Path
			if other, taken := routes[key]; taken {
				failed = append(failed, fmt.Sprintf("%s and %s share the route %s", other, c.Name, key))
			}
			routes[key] = c.Name
		}
	}
	ctx := context.Background()
	if _, err := r.Invoke(ctx, tool.Context{By: "terminal"}, "no-such-command-here", nil); code(err) != tool.CodeUnknownCommand {
		failed = append(failed, fmt.Sprintf("an unknown command gave %v, not unknown_command", err))
	}
	for _, c := range r.Commands() {
		if ok, _ := r.Available(c); !ok || !c.Offered(tool.CLI) {
			continue
		}
		if _, err := r.Invoke(ctx, tool.Context{By: "terminal"}, c.Name, json.RawMessage(`{"no_such_argument_here": 1}`)); code(err) != tool.CodeInvalidArgument {
			failed = append(failed, fmt.Sprintf("%s with an unknown argument gave %v, not invalid_argument", c.Name, err))
		}
		break
	}
	for _, c := range r.Commands() {
		if ok, _ := r.Available(c); ok {
			continue
		}
		by := map[tool.Interface]string{tool.CLI: "terminal", tool.TUI: "tui", tool.MCP: "mcp", tool.REST: "web"}
		ifaces := c.Interfaces
		if ifaces == nil {
			ifaces = tool.AllInterfaces
		}
		if _, err := r.Invoke(ctx, tool.Context{By: by[ifaces[0]]}, c.Name, nil); code(err) != tool.CodeFeatureUnavailable {
			failed = append(failed, fmt.Sprintf("%s needs an unavailable feature but gave %v", c.Name, err))
		}
		break
	}
	return failed
}

func code(err error) string {
	if err == nil {
		return ""
	}
	return tool.AsError(err).Code
}

// Step is one call, by an interface (By as a Context names it: terminal,
// tui, mcp, web).
type Step struct {
	By      string
	Command string
	Input   map[string]any
}

// Driver runs a step and returns its outcome as a JSON object: the result's
// fields or {"error": ...}, without the shared-state envelope.
type Driver interface {
	Call(s Step) map[string]any
}

// Direct calls the registry itself.
func Direct(r *tool.Registry) Driver { return direct{r} }

type direct struct{ r *tool.Registry }

func (d direct) Call(s Step) map[string]any {
	raw, _ := json.Marshal(s.Input)
	cx := tool.Context{By: s.By}
	out, err := d.r.Invoke(context.Background(), cx, s.Command, raw)
	return roundTrip(tool.Body(out, err, cx.Interface()))
}

// Interfaces sends each step through the interface its caller uses: MCP
// steps through the server (over an in-memory transport), web steps
// through the HTTP handler (with token), others straight to the registry.
func Interfaces(r *tool.Registry, server *mcp.Server, handler http.Handler, token string) (Driver, error) {
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		return nil, err
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "conformance", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		return nil, err
	}
	return interfaces{r: r, mcp: cs, http: handler, token: token}, nil
}

type interfaces struct {
	r     *tool.Registry
	mcp   *mcp.ClientSession
	http  http.Handler
	token string
}

func (d interfaces) Call(s Step) map[string]any {
	switch tool.InterfaceOf(s.By) {
	case tool.MCP:
		res, err := d.mcp.CallTool(context.Background(), &mcp.CallToolParams{Name: toolmcp.ToolName(s.Command), Arguments: orEmpty(s.Input)})
		if err != nil {
			// Not a tool: the server doesn't offer it to MCP.
			return map[string]any{"error": map[string]any{"code": "no_tool", "message": err.Error()}}
		}
		var body map[string]any
		_ = json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &body)
		return strip(body)
	case tool.REST:
		return d.rest(s)
	}
	return direct{d.r}.Call(s)
}

var param = regexp.MustCompile(`\{(\w+)\}`)

func (d interfaces) rest(s Step) map[string]any {
	c := d.r.Command(s.Command)
	if c == nil || !c.Offered(tool.REST) {
		return direct{d.r}.Call(s)
	}
	rt := rest.RouteOf(c)
	in := map[string]any{}
	for k, v := range s.Input {
		in[k] = v
	}
	path := param.ReplaceAllStringFunc(rt.Path, func(m string) string {
		name := m[1 : len(m)-1]
		v := fmt.Sprint(in[name])
		delete(in, name)
		return url.PathEscape(v)
	})
	var body io.Reader
	if rt.Method == http.MethodGet || rt.Method == http.MethodDelete {
		q := url.Values{}
		keys := make([]string, 0, len(in))
		for k := range in {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if list, ok := in[k].([]any); ok {
				for _, x := range list {
					q.Add(k, fmt.Sprint(x))
				}
				continue
			}
			if list, ok := in[k].([]string); ok {
				for _, x := range list {
					q.Add(k, x)
				}
				continue
			}
			q.Set(k, fmt.Sprint(in[k]))
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
	} else {
		data, _ := json.Marshal(in)
		body = strings.NewReader(string(data))
	}
	req := httptest.NewRequest(rt.Method, path, body)
	req.Header.Set("Authorization", "Bearer "+d.token)
	rw := httptest.NewRecorder()
	d.http.ServeHTTP(rw, req)
	data, _ := io.ReadAll(rw.Result().Body)
	if rt.Bytes != nil && rw.Code == http.StatusOK {
		return roundTrip(map[string]any{rt.Bytes.Data: base64.StdEncoding.EncodeToString(data), rt.Bytes.ContentType: rw.Header().Get("Content-Type")})
	}
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return strip(out)
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func strip(m map[string]any) map[string]any {
	for _, k := range tool.Envelope {
		delete(m, k)
	}
	return m
}

func roundTrip(m map[string]any) map[string]any {
	data, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// Compare runs the steps through both drivers in order and returns where
// their outcomes differ, after normalize (for times and the like).
func Compare(a, b Driver, steps []Step, normalize func(map[string]any)) []string {
	var diffs []string
	for i, s := range steps {
		x, y := a.Call(s), b.Call(s)
		if normalize != nil {
			normalize(x)
			normalize(y)
		}
		if !reflect.DeepEqual(x, y) {
			xj, _ := json.Marshal(x)
			yj, _ := json.Marshal(y)
			diffs = append(diffs, fmt.Sprintf("step %d (%s by %s):\n  first:  %s\n  second: %s", i, s.Command, s.By, xj, yj))
		}
	}
	return diffs
}

// DropKeys returns a normalizer that removes these keys wherever they appear
// (timestamps, say).
func DropKeys(keys ...string) func(map[string]any) {
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for k := range t {
				if slices.Contains(keys, k) {
					delete(t, k)
				} else {
					walk(t[k])
				}
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	return func(m map[string]any) { walk(m) }
}
