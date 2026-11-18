package rest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gerbillinae/go-tool-layers/cli"
	"github.com/gerbillinae/go-tool-layers/tool"
)

// Status says whether the site is running, and its address (with the
// token: give it to the user whole).
type Status struct {
	Running bool    `json:"running"`
	URL     *string `json:"url"`
}

// Site is the listener: it owns the socket and the network policy (localhost
// only, the Origin and Host checks, CORS for allowed origins), and knows
// nothing of routes. `serve` runs it, and the MCP server starts it on
// request (web-start), so both act on the same state.
type Site struct {
	Name         string
	AllowOrigins []string
	// Handler builds the HTTP interface for an address and token.
	Handler func(url, token string) http.Handler

	mu    sync.Mutex
	srv   *http.Server
	url   string
	token string
}

func (s *Site) status() Status {
	if s.srv == nil {
		return Status{}
	}
	u := s.url + "/?token=" + s.token
	return Status{Running: true, URL: &u}
}

// Status reports whether the site is running, and its address.
func (s *Site) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status()
}

// Address is the base URL and token ("" if not running).
func (s *Site) Address() (url, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url, s.token
}

// Start listens on 127.0.0.1:port (0: a free one) with token ("": a random
// one), or returns the running site's address.
func (s *Site) Start(port int, token string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return s.status(), nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		if addrInUse(err) {
			return Status{}, tool.Errorf(CodePortInUse, "port %d is already in use: pick another with --port, or leave it out for a free one", port)
		}
		return Status{}, tool.Errorf(tool.CodeInternal, "couldn't open a local port: %v", err)
	}
	if token == "" {
		secret := make([]byte, 24)
		if _, err := rand.Read(secret); err != nil {
			ln.Close()
			return Status{}, err
		}
		token = hex.EncodeToString(secret)
	}
	s.token, s.url = token, "http://"+ln.Addr().String()
	s.srv = &http.Server{Handler: Guard(s.url, s.AllowOrigins, s.Handler(s.url, token)), ReadHeaderTimeout: 10 * time.Second}
	srv := s.srv
	go func() { _ = srv.Serve(ln) }()
	cli.Logf("REST server at %s", s.url)
	return s.status(), nil
}

// Stop closes the site; open pages lose their connection.
func (s *Site) Stop() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		// Close, not Shutdown: event streams never end by themselves.
		_ = s.srv.Close()
		s.srv, s.url, s.token = nil, "", ""
		cli.Logf("REST server stopped")
	}
	return s.status()
}

// Guard checks the Origin header (against other sites) and the Host header
// (against DNS rebinding), and answers CORS for the allowed origins, so a
// page elsewhere can mount the raw web component.
func Guard(url string, allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && origin != url {
			if !slices.Contains(allowed, origin) {
				writeJSON(rw, http.StatusForbidden, tool.Body(nil, tool.Errorf(CodeForbiddenOrigin,
					"requests from %s aren't allowed: start the server with --allow-origin %s to allow them", origin, origin), tool.REST))
				return
			}
			rw.Header().Set("Access-Control-Allow-Origin", origin)
			rw.Header().Set("Vary", "Origin")
			if r.Method == http.MethodOptions {
				rw.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE")
				rw.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				rw.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if r.Host != strings.TrimPrefix(url, "http://") {
			writeJSON(rw, http.StatusForbidden, tool.Body(nil, tool.Errorf(CodeForbiddenOrigin, "unexpected host %q", r.Host), tool.REST))
			return
		}
		next.ServeHTTP(rw, r)
	})
}

// WebCommands are web-start and web-stop over a site, for MCP (and a TUI):
// starting the same HTTP interface `serve` runs, in the same process, so the
// agent and a page share state. The descriptions are the tool's: say what
// the page is for, and tell the agent to give the user the link in full.
func WebCommands(site *Site, startDescription, stopDescription string) []*tool.Command {
	type startInput struct {
		Open bool `json:"open,omitempty" jsonschema:"Also open the page in the user's default browser."`
	}
	start := tool.Cmd("web-start", "Serve the web UI on this computer, and return its address.", func(_ context.Context, _ tool.Context, in startInput) (Status, error) {
		st, err := site.Start(0, "")
		if err == nil && in.Open && st.URL != nil {
			if err := OpenBrowser(*st.URL); err != nil {
				cli.Logf("couldn't open a browser: %v", err)
			}
		}
		return st, err
	}).Describe(startDescription).Write().Idempotent().Only(tool.MCP, tool.TUI).Errors(CodePortInUse)
	stop := tool.Cmd("web-stop", "Stop serving the web UI; open pages lose their connection.", func(context.Context, tool.Context, struct{}) (Status, error) {
		return site.Stop(), nil
	}).Describe(stopDescription).Write().Idempotent().Only(tool.MCP, tool.TUI)
	return []*tool.Command{start, stop}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	cmd := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	}
	return exec.Command(cmd, url).Start()
}

// Ready is the one line `serve` prints on stdout when it's ready.
type Ready struct {
	URL         string `json:"url"`
	ControlsURL string `json:"controls_url"`
	Token       string `json:"token"`
}
