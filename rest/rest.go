// Package rest serves a tool's registry over HTTP: one route per command
// offered to REST (the command's own route, or GET/POST /api/<name>), plus
// the browser's: the page and its files, and the event stream. Routes only
// adapt commands; they have no logic of their own. The listener (Site) is
// separate: localhost only, Origin and Host checks, a token. See
// opinionated/rest-server.md.
package rest

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/gerbillinae/go-tool-layers/cli"
	"github.com/gerbillinae/go-tool-layers/tool"
)

// Codes only the HTTP interface uses.
const (
	CodeUnauthorized    = "unauthorized"
	CodeForbiddenOrigin = "forbidden_origin"
	CodePortInUse       = "port_in_use"
)

// Register adds the HTTP interface's codes to a registry.
func Register(r *tool.Registry) {
	r.Error(CodeUnauthorized, tool.Refused, "The API token is missing or wrong.")
	r.Error(CodeForbiddenOrigin, tool.Refused, "Requests from that origin (or host) aren't allowed.")
	r.Error(CodePortInUse, tool.Unavailable, "The port is already in use.")
}

// Stream feeds the event stream (GET /api/events): server-sent events, the
// state now and after every change.
type Stream struct {
	// Changed is closed at the next change.
	Changed func() <-chan struct{}
	// Message is what to send: the state, with the changes after last
	// (none on the first message), and its revision.
	Message func(last int64, first bool) (msg any, revision int64)
	// Page, if set, hears a page connect and disconnect.
	Page func(connected bool)
}

// Options for the handler.
type Options struct {
	Registry *tool.Registry
	Token    string
	// Files are the web UI's, served without the token at GET /.
	Files fs.FS
	// Reporter, if the tool has shared state: results then carry the
	// changes since REST's last call.
	Reporter *tool.Reporter
	Stream   *Stream
}

// RouteOf is the route a command is served on.
func RouteOf(c *tool.Command) *tool.Route {
	if c.Route != nil {
		return c.Route
	}
	if c.Writes {
		return tool.POST("/api/" + c.Name)
	}
	return tool.GET("/api/" + c.Name)
}

// Handler builds the HTTP interface.
func Handler(o Options) http.Handler {
	r := o.Registry
	mux := http.NewServeMux()
	if o.Files != nil {
		mux.Handle("GET /", http.FileServerFS(o.Files))
	}
	for _, c := range r.Commands() {
		if !c.Offered(tool.REST) {
			continue
		}
		route := RouteOf(c)
		mux.HandleFunc(route.Method+" "+route.Path, authed(o.Token, r, func(rw http.ResponseWriter, req *http.Request) {
			command(rw, req, o, c, route)
		}))
	}
	if o.Stream != nil {
		mux.HandleFunc("GET /api/events", authed(o.Token, r, func(rw http.ResponseWriter, req *http.Request) {
			events(rw, req, r, o.Stream)
		}))
	}
	return mux
}

var param = regexp.MustCompile(`\{(\w+)\}`)

// command adapts one request to a command: input from the path, the query
// (GET, DELETE) or the JSON body; the result as JSON with the changes, or
// raw for a bytes route.
func command(rw http.ResponseWriter, req *http.Request, o Options, c *tool.Command, route *tool.Route) {
	r := o.Registry
	fields := map[string]tool.Field{}
	for _, f := range tool.Fields(c.In) {
		fields[f.Name] = f
	}
	args := map[string]any{}
	if req.Method == http.MethodGet || req.Method == http.MethodDelete {
		q := req.URL.Query()
		q.Del("token")
		for k, vs := range q {
			args[k] = typed(fields[k].Type, vs)
		}
	} else {
		body, _ := io.ReadAll(req.Body)
		if strings.TrimSpace(string(body)) != "" {
			if err := json.Unmarshal(body, &args); err != nil {
				fail(rw, r, tool.Errorf(tool.CodeInvalidArgument, "the request body for %s isn't a JSON object: %v. Nothing was changed.", req.URL.Path, err))
				return
			}
		}
	}
	for _, m := range param.FindAllStringSubmatch(route.Path, -1) {
		args[m[1]] = typed(fields[m[1]].Type, []string{req.PathValue(m[1])})
	}
	raw, _ := json.Marshal(args)
	out, err := r.Invoke(req.Context(), tool.Context{By: "web", Reporter: o.Reporter, Progress: func(m string) { cli.Logf("%s", m) }}, c.Name, raw)
	if err == nil && route.Bytes != nil {
		body := tool.Body(out, nil, tool.REST)
		data, derr := base64.StdEncoding.DecodeString(fmt.Sprint(body[route.Bytes.Data]))
		if derr == nil {
			rw.Header().Set("Content-Type", fmt.Sprint(body[route.Bytes.ContentType]))
			_, _ = rw.Write(data)
			return
		}
		err = tool.Errorf(tool.CodeInternal, "%s's result has no bytes in %q", c.Name, route.Bytes.Data)
	}
	var body map[string]any
	if c.OwnChanges {
		body = tool.Body(out, err, tool.REST)
	} else {
		body = tool.WithChanges(o.Reporter, out, err, tool.REST)
	}
	status := http.StatusOK
	if err != nil {
		status = HTTPStatus(r, tool.AsError(err).Code)
	}
	writeJSON(rw, status, body)
}

// typed turns text from a URL into the JSON value a field of type t takes,
// so ?limit=3 is a number and ?decks=A&decks=B a list. An unknown field
// stays text, and Invoke refuses it.
func typed(t reflect.Type, vs []string) any {
	one := vs[len(vs)-1]
	if t == nil {
		return one
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice:
		return vs
	case reflect.Bool:
		if b, err := strconv.ParseBool(one); err == nil {
			return b
		}
	case reflect.Int, reflect.Int64, reflect.Int32:
		if n, err := strconv.ParseInt(one, 10, 64); err == nil {
			return n
		}
	case reflect.Float64, reflect.Float32:
		if n, err := strconv.ParseFloat(one, 64); err == nil {
			return n
		}
	}
	return one
}

// events streams the shared state as server-sent events: now, then after
// every change. It stops as soon as a write fails or the client goes, and
// tells the tool (Page), so "two pages open" stays true.
func events(rw http.ResponseWriter, req *http.Request, r *tool.Registry, s *Stream) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		fail(rw, r, tool.Errorf(tool.CodeInternal, "streaming isn't supported"))
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-store")
	if s.Page != nil {
		s.Page(true)
		defer s.Page(false)
	}
	var last int64
	first := true
	for {
		changed := s.Changed()
		msg, rev := s.Message(last, first)
		first, last = false, rev
		data, _ := json.Marshal(msg)
		if _, err := fmt.Fprintf(rw, "data: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-req.Context().Done():
			return
		case <-changed:
		}
	}
}

// HTTPStatus is the HTTP status for an error code, by its category.
func HTTPStatus(r *tool.Registry, code string) int {
	switch code {
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbiddenOrigin:
		return http.StatusForbidden
	}
	switch r.Category(code) {
	case tool.InvalidInput:
		return http.StatusBadRequest
	case tool.NotFound:
		return http.StatusNotFound
	case tool.Conflict:
		return http.StatusConflict
	case tool.Unavailable:
		return http.StatusBadGateway
	case tool.Refused:
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}

// authed requires the token, as a bearer token or (for event streams, which
// can't send headers) a token query parameter, compared in constant time.
func authed(token string, r *tool.Registry, h http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, req *http.Request) {
		got := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = req.URL.Query().Get("token")
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			fail(rw, r, tool.Errorf(CodeUnauthorized, "missing or wrong token: use the token the server gave when it started"))
			return
		}
		h(rw, req)
	}
}

func writeJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func fail(rw http.ResponseWriter, r *tool.Registry, err error) {
	writeJSON(rw, HTTPStatus(r, tool.AsError(err).Code), tool.Body(nil, err, tool.REST))
}
