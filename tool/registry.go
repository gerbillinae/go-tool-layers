package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Route adapts a command to HTTP: the route only changes the transport.
// Path parameters ({name}) name input fields.
type Route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Bytes, if set, sends one result field as the raw body, with another
	// as its Content-Type, instead of JSON.
	Bytes *BytesFields `json:"bytes,omitempty"`
}

// BytesFields names a result's data field (bytes, base64 in JSON) and its
// content type field.
type BytesFields struct {
	Data        string `json:"data"`
	ContentType string `json:"content_type"`
}

func GET(path string) *Route    { return &Route{Method: "GET", Path: path} }
func POST(path string) *Route   { return &Route{Method: "POST", Path: path} }
func PATCH(path string) *Route  { return &Route{Method: "PATCH", Path: path} }
func DELETE(path string) *Route { return &Route{Method: "DELETE", Path: path} }

// Raw sends the result's data field as the body, with contentType's field
// as its Content-Type.
func (r *Route) Raw(data, contentType string) *Route {
	r.Bytes = &BytesFields{Data: data, ContentType: contentType}
	return r
}

// Command is one command: registered once, offered to every interface that
// should have it.
type Command struct {
	Name        string
	Summary     string
	LongText    string
	Description string // for agents: when and how to use it
	Examples    []string
	// Usage is the CLI's usage line after the tool's name, e.g. "add-card
	// --deck DECK --front TEXT --back TEXT [flags]".
	Usage      string
	Writes     bool
	Idem       bool
	Interfaces []Interface
	Required   []string // features
	Codes      []string
	Route      *Route
	// OwnChanges: the result reports the shared changes itself (a state
	// snapshot), so interfaces don't add them.
	OwnChanges bool
	In         reflect.Type
	run        func(ctx context.Context, cx Context, in any) (any, error)
}

// Cmd registers a typed command: f gets the decoded input.
func Cmd[I, O any](name, summary string, f func(ctx context.Context, cx Context, in I) (O, error)) *Command {
	return &Command{
		Name:    name,
		Summary: summary,
		In:      reflect.TypeFor[I](),
		run: func(ctx context.Context, cx Context, in any) (any, error) {
			return f(ctx, cx, *in.(*I))
		},
	}
}

func (c *Command) Long(s string) *Command                  { c.LongText = s; return c }
func (c *Command) Describe(s string) *Command              { c.Description = s; return c }
func (c *Command) Example(s string) *Command               { c.Examples = append(c.Examples, s); return c }
func (c *Command) Use(s string) *Command                   { c.Usage = s; return c }
func (c *Command) Write() *Command                         { c.Writes = true; return c }
func (c *Command) Idempotent() *Command                    { c.Idem = true; return c }
func (c *Command) Only(i ...Interface) *Command            { c.Interfaces = i; return c }
func (c *Command) Requires(features ...string) *Command    { c.Required = features; return c }
func (c *Command) Errors(codes ...string) *Command         { c.Codes = append(c.Codes, codes...); return c }
func (c *Command) HTTP(r *Route) *Command                  { c.Route = r; return c }
func (c *Command) ReportsOwnChanges() *Command             { c.OwnChanges = true; return c }
func (c *Command) Offered(i Interface) bool                { return c.Interfaces == nil || slices.Contains(c.Interfaces, i) }
func (c *Command) interfaces() []Interface {
	if c.Interfaces == nil {
		return AllInterfaces
	}
	return c.Interfaces
}

// ErrorInfo describes an error code.
type ErrorInfo struct {
	Category    Category `json:"category"`
	Description string   `json:"description"`
}

// ConfigItem is a tool-wide setting (AnkiConnect's address): a CLI flag with
// an environment variable, passed to the tool's configure function.
type ConfigItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Default     string `json:"default,omitempty"`
	Env         string `json:"env,omitempty"`
}

// Registry holds a tool's commands.
type Registry struct {
	Name, Version, Summary, LongText string
	Config                           []ConfigItem
	Configure                        func(settings map[string]string) error
	commands                         []*Command
	errors                           map[string]ErrorInfo
	features                         func() map[string]bool
	schemas                          map[string]*jsonschema.Schema
}

// New makes a registry with the codes every tool has.
func New(name, version, summary string) *Registry {
	r := &Registry{Name: name, Version: version, Summary: summary, errors: map[string]ErrorInfo{}, schemas: map[string]*jsonschema.Schema{}}
	r.Error(CodeInvalidArgument, InvalidInput, "An argument is malformed or unknown.")
	r.Error(CodeMissingArgument, InvalidInput, "A required argument is missing.")
	r.Error(CodeUnknownCommand, NotFound, "No command by that name.")
	r.Error(CodeNotAvailableHere, Refused, "The command isn't offered to this interface.")
	r.Error(CodeFeatureUnavailable, Unavailable, "The command needs something this setup doesn't provide.")
	r.Error(CodeInternal, Internal, "Something went wrong inside the tool.")
	return r
}

// Long sets the tool's longer description.
func (r *Registry) Long(s string) *Registry { r.LongText = s; return r }

// Error registers a code with its category.
func (r *Registry) Error(code string, c Category, description string) *Registry {
	r.errors[code] = ErrorInfo{Category: c, Description: description}
	return r
}

// Features sets what's available here (commands Require them).
func (r *Registry) Features(f func() map[string]bool) *Registry { r.features = f; return r }

// Setting adds a tool-wide setting.
func (r *Registry) Setting(name, description, def, env string) *Registry {
	r.Config = append(r.Config, ConfigItem{Name: name, Description: description, Default: def, Env: env})
	return r
}

// Add registers commands. A name registered twice panics: that's a bug.
func (r *Registry) Add(cmds ...*Command) *Registry {
	for _, c := range cmds {
		if r.Command(c.Name) != nil {
			panic("tool: command registered twice: " + c.Name)
		}
		s, err := jsonschema.ForType(c.In, &jsonschema.ForOptions{})
		if err != nil {
			panic(fmt.Sprintf("tool: no schema for %s's input: %v", c.Name, err))
		}
		r.schemas[c.Name] = s
		r.commands = append(r.commands, c)
	}
	return r
}

// Commands are the registered commands, in order.
func (r *Registry) Commands() []*Command { return r.commands }

// Command is a command by name, or nil.
func (r *Registry) Command(name string) *Command {
	for _, c := range r.commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Schema is a command's input schema.
func (r *Registry) Schema(name string) *jsonschema.Schema { return r.schemas[name] }

// Category is a code's category (internal if unregistered).
func (r *Registry) Category(code string) Category {
	if e, ok := r.errors[code]; ok {
		return e.Category
	}
	return Internal
}

// Available reports whether every feature a command requires is here.
func (r *Registry) Available(c *Command) (bool, string) {
	features := r.Feature()
	for _, f := range c.Required {
		if !features[f] {
			return false, f
		}
	}
	return true, ""
}

// Feature is what's available now.
func (r *Registry) Feature() map[string]bool {
	if r.features == nil {
		return map[string]bool{}
	}
	return r.features()
}

// Description is the whole tool, as every interface and test reads it.
type Description struct {
	Name     string               `json:"name"`
	Version  string               `json:"version"`
	Summary  string               `json:"summary"`
	Long     string               `json:"long,omitempty"`
	Config   []ConfigItem         `json:"config,omitempty"`
	Features map[string]bool      `json:"features"`
	Commands []CommandInfo        `json:"commands"`
	Errors   map[string]ErrorInfo `json:"errors"`
}

// CommandInfo is one command in a Description.
type CommandInfo struct {
	Name        string             `json:"name"`
	Summary     string             `json:"summary"`
	Long        string             `json:"long,omitempty"`
	Description string             `json:"description,omitempty"`
	Examples    []string           `json:"examples,omitempty"`
	Writes      bool               `json:"writes,omitempty"`
	Idempotent  bool               `json:"idempotent,omitempty"`
	Interfaces  []Interface        `json:"interfaces"`
	Requires    []string           `json:"requires,omitempty"`
	Errors      []string           `json:"errors,omitempty"`
	Route       *Route             `json:"route,omitempty"`
	Input       *jsonschema.Schema `json:"input"`
}

// Describe returns the tool's description.
func (r *Registry) Describe() Description {
	d := Description{Name: r.Name, Version: r.Version, Summary: r.Summary, Long: r.LongText, Config: r.Config, Features: r.Feature(), Errors: r.errors}
	for _, c := range r.commands {
		d.Commands = append(d.Commands, CommandInfo{
			Name: c.Name, Summary: c.Summary, Long: c.LongText, Description: c.Description, Examples: c.Examples,
			Writes: c.Writes, Idempotent: c.Idem, Interfaces: c.interfaces(), Requires: c.Required, Errors: c.Codes,
			Route: c.Route, Input: r.schemas[c.Name],
		})
	}
	return d
}

// Invoke runs a command for an interface: it checks the command is offered
// there and available, decodes the input (refusing unknown arguments,
// arguments not offered to the interface, and missing required ones), and
// runs it. A panic in the command becomes an internal error, so one bad call
// can't take down a long-running server.
func (r *Registry) Invoke(ctx context.Context, cx Context, name string, input json.RawMessage) (out any, err error) {
	c := r.Command(name)
	iface := cx.Interface()
	if c == nil {
		return nil, Errorf(CodeUnknownCommand, "%s has no command %q", r.Name, name)
	}
	if !c.Offered(iface) {
		return nil, Errorf(CodeNotAvailableHere, "{cmd:%s} isn't offered to %s callers", name, iface)
	}
	if ok, missing := r.Available(c); !ok {
		return nil, Errorf(CodeFeatureUnavailable, "{cmd:%s} isn't available here: it needs %s", name, missing)
	}
	in, err := Decode(c.In, input, iface)
	if err != nil {
		return nil, err
	}
	defer func() {
		if p := recover(); p != nil {
			out, err = nil, Errorf(CodeInternal, "the tool failed in {cmd:%s}: %v", name, p)
		}
	}()
	return c.run(ctx, cx, in)
}

// Field is one input field, by its JSON name.
type Field struct {
	Name       string
	Index      []int
	Type       reflect.Type
	Tag        reflect.StructTag
	Required   bool
	Interfaces []Interface // nil: all
}

// Fields lists a struct type's input fields, flattening embedded structs as
// encoding/json does.
func Fields(t reflect.Type) []Field {
	var out []Field
	var walk func(t reflect.Type, index []int)
	walk = func(t reflect.Type, index []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			idx := append(slices.Clone(index), i)
			tag := f.Tag.Get("json")
			name, opts, _ := strings.Cut(tag, ",")
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				walk(f.Type, idx)
				continue
			}
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			field := Field{Name: name, Index: idx, Type: f.Type, Tag: f.Tag, Required: !strings.Contains(opts, "omitempty") && f.Type.Kind() != reflect.Pointer}
			if s := f.Tag.Get("interfaces"); s != "" {
				for _, x := range strings.Split(s, ",") {
					field.Interfaces = append(field.Interfaces, Interface(strings.TrimSpace(x)))
				}
			}
			out = append(out, field)
		}
	}
	if t.Kind() == reflect.Struct {
		walk(t, nil)
	}
	return out
}

// Decode reads input (a JSON object) into a new value of type t for an
// interface, returning a pointer to it. Unknown arguments, arguments not
// offered to the interface (the `interfaces:"mcp,rest"` tag) and missing
// required ones (no omitempty) are refused.
func Decode(t reflect.Type, input json.RawMessage, iface Interface) (any, error) {
	if len(strings.TrimSpace(string(input))) == 0 || string(input) == "null" {
		input = json.RawMessage("{}")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, Errorf(CodeInvalidArgument, "the arguments aren't a JSON object: %v. Nothing was changed.", err)
	}
	fields := Fields(t)
	known := map[string]Field{}
	for _, f := range fields {
		known[f.Name] = f
	}
	for k := range raw {
		f, ok := known[k]
		if !ok || (f.Interfaces != nil && !slices.Contains(f.Interfaces, iface)) {
			return nil, Errorf(CodeInvalidArgument, "there's no argument {arg:%s}. Nothing was changed.", k)
		}
	}
	for _, f := range fields {
		if _, given := raw[f.Name]; f.Required && !given && (f.Interfaces == nil || slices.Contains(f.Interfaces, iface)) {
			return nil, Errorf(CodeMissingArgument, "{arg:%s} is required. Nothing was changed.", f.Name)
		}
	}
	v := reflect.New(t)
	if err := json.Unmarshal(input, v.Interface()); err != nil {
		return nil, Errorf(CodeInvalidArgument, "invalid arguments: %v. Nothing was changed.", err)
	}
	return v.Interface(), nil
}
