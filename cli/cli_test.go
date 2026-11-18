package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gerbillinae/go-tool-layers/tool"
)

type voice string

type in struct {
	Deck   string   `json:"deck" flag:"deck" jsonschema:"The deck."`
	Tags   []string `json:"tags,omitempty" flag:"tag"`
	Speed  *float64 `json:"speed,omitempty" flag:"speed"`
	Voice  voice    `json:"voice,omitempty" flag:"voice"`
	DryRun bool     `json:"dry_run,omitempty" flag:"dry-run"`
	Create bool     `json:"create_missing,omitempty" flag:"create-missing" yes:"true"`
	File   string   `json:"file,omitempty" flag:"file" file:"true"`
	Cards  []string `json:"cards,omitempty" interfaces:"mcp,rest"`
}

func TestGeneratedCommand(t *testing.T) {
	var got map[string]any
	r := tool.New("t", "1", "test")
	r.Add(tool.Cmd("add", "Add.", func(_ context.Context, cx tool.Context, x in) (in, error) {
		data, _ := json.Marshal(x)
		_ = json.Unmarshal(data, &got)
		return x, nil
	}))
	f := filepath.Join(t.TempDir(), "cards.yaml")
	_ = os.WriteFile(f, []byte("hello"), 0o644)
	root := Root(Options{Registry: r})
	root.SetArgs([]string{"add", "--deck", "A", "--tag", "x", "--tag", "y", "--speed", "0.8", "--voice", "male", "--dry-run", "--yes", "--file", f})
	var out bytes.Buffer
	root.SetOut(&out)
	stdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	err := root.Execute()
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	if got["deck"] != "A" || len(got["tags"].([]any)) != 2 || got["speed"] != 0.8 || got["voice"] != "male" ||
		got["dry_run"] != true || got["create_missing"] != true || got["file"] != "hello" {
		t.Errorf("%v", got)
	}
	root = Root(Options{Registry: r})
	root.SetArgs([]string{"add"})
	if err := root.Execute(); err == nil || tool.AsError(Error(err)).Code != tool.CodeMissingArgument {
		t.Errorf("missing --deck: %v", err)
	}
}

func TestRendersPlaceholdersForTheCLI(t *testing.T) {
	if got := tool.Render("pass {arg:create_missing}", tool.CLI); got != "pass --create-missing" {
		t.Error(got)
	}
}
