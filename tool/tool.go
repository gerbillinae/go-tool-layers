// Package tool is the contract every interface of a tool shares: commands
// registered once (with their options, results, errors and the interfaces
// that offer them), a description of the whole tool, and Invoke, which every
// interface (CLI, MCP, REST, a TUI) calls. See opinionated/layering.md.
package tool

import (
	"errors"
	"fmt"
	"strings"
)

// Interface is a way of reaching the tool.
type Interface string

const (
	CLI  Interface = "cli"
	TUI  Interface = "tui"
	MCP  Interface = "mcp"
	REST Interface = "rest"
)

// AllInterfaces is the default for a command: offered everywhere.
var AllInterfaces = []Interface{CLI, TUI, MCP, REST}

// InterfaceOf is the interface a call's By names: terminal (the CLI), tui,
// mcp, web (REST).
func InterfaceOf(by string) Interface {
	switch by {
	case "terminal", "cli":
		return CLI
	case "tui":
		return TUI
	case "mcp":
		return MCP
	}
	return REST
}

// Category groups error codes, so every interface can map any tool's codes
// to an exit status or an HTTP status without knowing them.
type Category string

const (
	InvalidInput Category = "invalid_input"
	NotFound     Category = "not_found"
	Conflict     Category = "conflict"
	Unavailable  Category = "unavailable"
	Refused      Category = "refused"
	Internal     Category = "internal"
)

// Codes every tool has.
const (
	CodeInvalidArgument    = "invalid_argument"
	CodeMissingArgument    = "missing_argument"
	CodeUnknownCommand     = "unknown_command"
	CodeNotAvailableHere   = "not_available_here"
	CodeFeatureUnavailable = "feature_unavailable"
	CodeInternal           = "internal"
)

// Error is an error with a stable code, which every interface reports: as
// {"error": {"code", "message"}} on the CLI's stdout, an MCP tool error, an
// HTTP error body.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details, if any, is JSON-marshalled with the error.
	Details any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Errorf makes an Error with a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsError returns err as an *Error; an error without a code is internal.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// Extra is an error that adds fields to the result body MCP and REST send
// (e.g. a state_changed error carrying the current state).
type Extra interface {
	Extra() map[string]any
}

// Asker asks the person at the other end a question. Only a call with
// someone to answer has one; otherwise each question's argument answers it.
type Asker interface {
	Confirm(question string) (bool, error)
	Ask(question string) (string, error)
}

// Context is a call's own facts: who's calling, whether someone can answer
// questions, where its progress goes, and (for MCP and REST) the reporter
// that tracks which changes the caller has seen.
type Context struct {
	// By is terminal, tui, mcp or web.
	By string
	// Asker is nil when no one can answer.
	Asker Asker
	// Progress receives messages for a person about this call.
	Progress func(message string)
	// Reporter is the caller's place in the shared change log, if the
	// interface keeps one.
	Reporter *Reporter
}

// Logf sends a progress message for this call (to stderr, MCP progress, …).
func (cx Context) Logf(format string, args ...any) {
	if cx.Progress != nil {
		cx.Progress(fmt.Sprintf(format, args...))
	}
}

// Interface is the interface this call came through.
func (cx Context) Interface() Interface { return InterfaceOf(cx.By) }

// Render writes a message's placeholders as the interface spells them:
// {arg:create_missing} is --create-missing on the CLI and create_missing
// elsewhere; {cmd:list-cards} is list-cards on the CLI and a TUI, list_cards
// for MCP and REST.
func Render(text string, i Interface) string {
	for _, kind := range []string{"arg", "cmd"} {
		for {
			start := strings.Index(text, "{"+kind+":")
			if start < 0 {
				break
			}
			end := strings.Index(text[start:], "}")
			if end < 0 {
				break
			}
			name := text[start+len(kind)+2 : start+end]
			text = text[:start] + spell(kind, name, i) + text[start+end+1:]
		}
	}
	return text
}

func spell(kind, name string, i Interface) string {
	switch {
	case kind == "arg" && (i == CLI || i == TUI):
		return "--" + strings.ReplaceAll(name, "_", "-")
	case kind == "arg":
		return name
	case i == CLI || i == TUI:
		return name
	default:
		return strings.ReplaceAll(name, "-", "_")
	}
}
