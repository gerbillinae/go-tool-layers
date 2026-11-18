// Package cli builds a tool's command line from its registry (cobra): one
// subcommand per command offered to the CLI, one flag per input field with a
// `flag` tag, settings as global flags with environment variables. Every
// command prints one JSON value on stdout; errors are
// {"error": {"code", "message"}} on stdout, a line on stderr, and exit
// status 1. See opinionated/cli-tool-design.md.
//
// Field tags the CLI reads, beside json and jsonschema (the help text):
//
//	flag:"deck"       the flag's name (fields without one aren't CLI flags)
//	short:"d"         a one-letter alias
//	file:"true"       a string field the CLI fills from a file path, or - for stdin
//	yes:"true"        a bool field --yes sets (answering a confirmation)
//
// A field whose json tag lacks omitempty (and isn't a pointer) is a required
// flag. Only flags that were given are passed on, so the tool's defaults
// apply, the same as for MCP and REST.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/gerbillinae/go-tool-layers/tool"
)

// MCPMode is set while the tool serves MCP over stdio: stdout then carries
// only MCP messages, so errors go to stderr alone.
var MCPMode bool

// RawMode is set while a custom command has the terminal in raw mode, where
// a line needs an explicit carriage return.
var RawMode atomic.Bool

// Options for building the command line.
type Options struct {
	Registry *tool.Registry
	// Customs are hand-written commands (an interactive player, mcp,
	// serve), added, or replacing a generated one of the same name.
	Customs []*cobra.Command
}

// Root builds the tool's root command.
func Root(o Options) *cobra.Command {
	r := o.Registry
	root := &cobra.Command{
		Use:           r.Name,
		Short:         r.Summary,
		Long:          tool.Render(r.LongText, tool.CLI),
		Version:       r.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return Error(err) })
	settings := map[string]*string{}
	for _, c := range r.Config {
		def := c.Default
		if c.Env != "" {
			if v := os.Getenv(c.Env); v != "" {
				def = v
			}
		}
		help := c.Description
		if c.Env != "" {
			help += " (env " + c.Env + ")"
		}
		v := new(string)
		root.PersistentFlags().StringVar(v, strings.ReplaceAll(c.Name, "_", "-"), def, help)
		settings[c.Name] = v
	}
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		if r.Configure == nil {
			return nil
		}
		m := map[string]string{}
		for k, v := range settings {
			m[k] = *v
		}
		return r.Configure(m)
	}
	custom := map[string]bool{}
	for _, c := range o.Customs {
		custom[c.Name()] = true
	}
	for _, c := range r.Commands() {
		if c.Offered(tool.CLI) && !custom[c.Name] {
			root.AddCommand(Command(r, c))
		}
	}
	root.AddCommand(o.Customs...)
	return root
}

// Execute runs the root command with a context that SIGINT and SIGTERM
// cancel, and returns the exit status.
func Execute(root *cobra.Command) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		return Fail(Error(err))
	}
	return 0
}

// Command builds one generated subcommand.
func Command(r *tool.Registry, c *tool.Command) *cobra.Command {
	in := reflect.New(c.In)
	use := c.Usage
	if use == "" {
		use = c.Name
	}
	long := c.LongText
	if long == "" {
		long = c.Summary
	}
	cmd := &cobra.Command{
		Use:     use,
		Short:   c.Summary,
		Long:    tool.Render(long, tool.CLI),
		Example: strings.Join(c.Examples, "\n"),
		Args:    cobra.NoArgs,
	}
	if ok, missing := r.Available(c); !ok {
		cmd.Hidden = true
		cmd.Short += " (needs " + missing + ")"
	}
	bound := Bind(cmd, in.Interface())
	var yes bool
	hasYes := slices.ContainsFunc(tool.Fields(c.In), func(f tool.Field) bool { return f.Tag.Get("yes") == "true" })
	if hasYes {
		cmd.Flags().BoolVar(&yes, "yes", false, "Answer yes to every question.")
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		args, stdinUsed, err := Given(cmd, bound, in)
		if err != nil {
			return err
		}
		if yes {
			for _, f := range tool.Fields(c.In) {
				if f.Tag.Get("yes") == "true" {
					args[f.Name] = true
				}
			}
		}
		raw, _ := json.Marshal(args)
		cx := tool.Context{By: "terminal", Progress: func(m string) { Logf("%s", m) }}
		if !stdinUsed {
			if p := NewPrompter(); p != nil {
				cx.Asker = p
			}
		}
		out, err := r.Invoke(cmd.Context(), cx, c.Name, raw)
		if err != nil {
			return err
		}
		PrintJSON(out)
		return nil
	}
	return cmd
}

// Binding is a flag bound to an input field.
type Binding struct {
	Flag  string
	Field tool.Field
}

// Bind adds a flag for each field of the struct v points to that has a
// `flag` tag, and returns them. The help is the field's jsonschema tag, the
// same text MCP's schema shows, so the CLI and MCP can't drift apart.
func Bind(cmd *cobra.Command, v any) []Binding {
	rv := reflect.ValueOf(v).Elem()
	var out []Binding
	for _, f := range tool.Fields(rv.Type()) {
		name := f.Tag.Get("flag")
		if name == "" {
			continue
		}
		help := f.Tag.Get("jsonschema")
		fv := &fieldValue{v: rv.FieldByIndex(f.Index)}
		if fv.isFile = f.Tag.Get("file") == "true"; fv.isFile {
			help += " (a file, or - for stdin)"
		}
		if fv.kind().Kind() == reflect.Slice {
			help += " (repeatable)"
		}
		fl := cmd.Flags().VarPF(fv, name, f.Tag.Get("short"), help)
		if fv.kind().Kind() == reflect.Bool {
			fl.NoOptDefVal = "true"
		}
		if f.Required {
			_ = cmd.MarkFlagRequired(name)
		}
		out = append(out, Binding{Flag: name, Field: f})
	}
	return out
}

// Given returns the input fields whose flags were given, by JSON name, with
// file flags read (and whether one of them read stdin, which then can't
// answer questions).
func Given(cmd *cobra.Command, bound []Binding, in reflect.Value) (map[string]any, bool, error) {
	args := map[string]any{}
	stdin := false
	for _, b := range bound {
		if !cmd.Flags().Changed(b.Flag) {
			continue
		}
		v := in.Elem().FieldByIndex(b.Field.Index)
		if b.Field.Tag.Get("file") == "true" {
			path := v.String()
			var data []byte
			var err error
			if path == "-" {
				data, err = io.ReadAll(os.Stdin)
				stdin = true
			} else {
				data, err = os.ReadFile(path)
			}
			if err != nil {
				return nil, false, tool.Errorf(tool.CodeInvalidArgument, "can't read %s: %v. Nothing was changed.", path, err)
			}
			args[b.Field.Name] = string(data)
			continue
		}
		args[b.Field.Name] = v.Interface()
	}
	return args, stdin, nil
}

// fieldValue is a pflag.Value over a struct field of a basic kind (or a
// pointer to one, which stays nil unless given, or a slice, which repeats).
type fieldValue struct {
	v      reflect.Value
	isFile bool
}

func (f *fieldValue) kind() reflect.Type {
	t := f.v.Type()
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

func (f *fieldValue) String() string {
	if !f.v.IsValid() || (f.v.Kind() == reflect.Pointer && f.v.IsNil()) {
		return ""
	}
	v := reflect.Indirect(f.v)
	if v.Kind() == reflect.Slice {
		var s []string
		for i := range v.Len() {
			s = append(s, fmt.Sprint(v.Index(i).Interface()))
		}
		return strings.Join(s, ",")
	}
	if v.IsZero() {
		return ""
	}
	return fmt.Sprint(v.Interface())
}

func (f *fieldValue) Type() string {
	t := f.kind()
	if f.isFile {
		return "file"
	}
	switch t.Kind() {
	case reflect.Slice:
		return "strings"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int64, reflect.Int32:
		return "int"
	case reflect.Float64, reflect.Float32:
		return "float"
	}
	return "string"
}

func (f *fieldValue) Set(s string) error {
	t := f.kind()
	target := f.v
	if f.v.Kind() == reflect.Pointer {
		if f.v.IsNil() {
			f.v.Set(reflect.New(t))
		}
		target = f.v.Elem()
	}
	if t.Kind() == reflect.Slice {
		el := reflect.New(t.Elem()).Elem()
		if err := setBasic(el, s); err != nil {
			return err
		}
		target.Set(reflect.Append(target, el))
		return nil
	}
	return setBasic(target, s)
}

func setBasic(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("%q isn't true or false", s)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64, reflect.Int32:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("%q isn't a whole number", s)
		}
		v.SetInt(n)
	case reflect.Float64, reflect.Float32:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("%q isn't a number", s)
		}
		v.SetFloat(n)
	default:
		return fmt.Errorf("unsupported flag type %s", v.Type())
	}
	return nil
}

// Error gives cobra's own errors (bad flags, missing ones) a code.
func Error(err error) error {
	if _, ok := err.(*tool.Error); ok {
		return err
	}
	msg := err.Error()
	if strings.Contains(msg, "required flag") {
		return tool.Errorf(tool.CodeMissingArgument, "%s. Nothing was changed.", msg)
	}
	return tool.Errorf(tool.CodeInvalidArgument, "%s (see --help). Nothing was changed.", msg)
}

// PrintJSON writes a command's one result to stdout, indented.
func PrintJSON(v any) {
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(os.Stdout, string(out))
}

// Fail reports err and returns the exit status: the error as JSON on stdout
// (except in MCP mode) and, for a person, on stderr.
func Fail(err error) int {
	e := *tool.AsError(err)
	e.Message = tool.Render(e.Message, tool.CLI)
	if !MCPMode {
		PrintJSON(map[string]any{"error": e})
	}
	Logf("error [%s]: %s", e.Code, e.Message)
	return 1
}

// Logf writes a line for a person to stderr.
func Logf(format string, args ...any) {
	end := "\n"
	if RawMode.Load() {
		end = "\r\n"
	}
	fmt.Fprint(os.Stderr, fmt.Sprintf(format, args...)+end)
}

// StdinIsTerminal reports whether someone can answer questions.
func StdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// Prompter asks questions on the terminal: the question on stderr, the
// answer from stdin.
type Prompter struct{ in *bufio.Reader }

// NewPrompter returns a prompter, or nil without a terminal.
func NewPrompter() *Prompter {
	if !StdinIsTerminal() {
		return nil
	}
	return &Prompter{in: bufio.NewReader(os.Stdin)}
}

func (p *Prompter) Ask(question string) (string, error) {
	fmt.Fprint(os.Stderr, question)
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", tool.Errorf("not_confirmed", "no answer was given. Nothing was changed.")
	}
	return strings.TrimSpace(line), nil
}

func (p *Prompter) Confirm(question string) (bool, error) {
	a, err := p.Ask(question + " [y/N] ")
	if err != nil {
		return false, err
	}
	a = strings.ToLower(a)
	return a == "y" || a == "yes", nil
}
