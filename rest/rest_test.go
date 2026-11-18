package rest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gerbillinae/go-tool-layers/tool"
)

type mediaIn struct {
	File string `json:"file"`
}
type media struct {
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}
type cardIn struct {
	Draft string `json:"draft_id"`
	Card  int    `json:"card_id"`
	Limit *int64 `json:"limit,omitempty"`
}

func registry() *tool.Registry {
	r := tool.New("t", "1", "test")
	Register(r)
	r.Error("not_there", tool.NotFound, "Not there.")
	r.Add(
		tool.Cmd("get-media", "A file.", func(_ context.Context, _ tool.Context, in mediaIn) (media, error) {
			return media{ContentType: "audio/mpeg", Data: []byte("bytes of " + in.File)}, nil
		}).HTTP(tool.GET("/api/media/{file}").Raw("data", "content_type")),
		tool.Cmd("card", "A card.", func(_ context.Context, _ tool.Context, in cardIn) (cardIn, error) { return in, nil }).
			HTTP(tool.GET("/api/drafts/{draft_id}/cards/{card_id}")),
		tool.Cmd("missing", "Fails.", func(context.Context, tool.Context, struct{}) (any, error) {
			return nil, tool.Errorf("not_there", "nope")
		}),
		tool.Cmd("cli-only", "CLI.", func(context.Context, tool.Context, struct{}) (any, error) { return 1, nil }).Only(tool.CLI),
	)
	return r
}

func get(t *testing.T, h http.Handler, path, token string) (int, string) {
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	body, _ := io.ReadAll(rw.Result().Body)
	return rw.Code, string(body)
}

func TestRoutesAdaptCommands(t *testing.T) {
	h := Handler(Options{Registry: registry(), Token: "secret"})
	if code, body := get(t, h, "/api/media/a.mp3", "secret"); code != 200 || body != "bytes of a.mp3" {
		t.Errorf("raw route: %d %q", code, body)
	}
	code, body := get(t, h, "/api/drafts/d3/cards/2?limit=5", "secret")
	var got map[string]any
	_ = json.Unmarshal([]byte(body), &got)
	if code != 200 || got["card_id"] != 2.0 || got["draft_id"] != "d3" || got["limit"] != 5.0 {
		t.Errorf("path and query typed: %d %s", code, body)
	}
	if code, _ := get(t, h, "/api/missing", "secret"); code != 404 {
		t.Errorf("category to status: %d", code)
	}
	if code, _ := get(t, h, "/api/missing", "wrong"); code != 401 {
		t.Errorf("token: %d", code)
	}
	if code, _ := get(t, h, "/api/cli-only", "secret"); code != 404 {
		t.Errorf("a CLI-only command has no route: %d", code)
	}
	if code, body := get(t, h, "/api/drafts/d3/cards/2?nope=1", "secret"); code != 400 || !strings.Contains(body, "invalid_argument") {
		t.Errorf("unknown parameter: %d %s", code, body)
	}
}

func TestSiteGuards(t *testing.T) {
	s := &Site{Handler: func(string, string) http.Handler { return Handler(Options{Registry: registry(), Token: "secret"}) }}
	st, err := s.Start(0, "secret")
	if err != nil || !st.Running {
		t.Fatal(err)
	}
	defer s.Stop()
	url, _ := s.Address()
	req, _ := http.NewRequest("GET", url+"/api/missing", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %v %v", resp.StatusCode, err)
	}
	if again, _ := s.Start(0, ""); *again.URL != *st.URL {
		t.Error("starting again returns the running site")
	}
}
