# go-tool-layers

Go libraries for building a tool once and offering it as a CLI, an MCP server and a REST API with a web UI, all generated from one registry of its commands. The Go counterpart of [wasm-tool-layers](https://github.com/gerbillinae/wasm-tool-layers) (Rust), without the WebAssembly parts.

The design and its reasoning are in [opinionated](https://github.com/gerbillinae/opinionated): start with [layering.md](https://github.com/gerbillinae/opinionated/blob/main/layering.md) and [playbooks.md](https://github.com/gerbillinae/opinionated/blob/main/playbooks.md). The worked example is [anki-cards](https://github.com/gerbillinae/anki-cards).

## The packages

| Package | What it is |
|---|---|
| `tool` | The registry: commands registered once (typed, with routes, interfaces, errors, features), `Describe`, and `Invoke`, which checks the interface, unknown and missing arguments and features, and turns a panic into an internal error. Error codes with categories, `{arg:…}`/`{cmd:…}` placeholders, and the shared-state reporter (`changes_since_last_call`). |
| `cli` | A cobra command line generated from the registry: one subcommand per command, one flag per tagged field, settings as global flags with environment variables, JSON output and errors. |
| `mcp` (`toolmcp`) | MCP tools generated from the registry, over the official go-sdk, with the changes since each session's last call and progress notifications. |
| `rest` | Routes that only adapt commands (typed path and query parameters, raw-bytes routes), the event stream, and the listener (`Site`): localhost only, Origin and Host checks, CORS for allowed origins, a token. `WebCommands` gives `web-start`/`web-stop`. |
| `files` | Lock files and atomic writes, for data several processes share; `StandardDir`. |
| `conformance` | `Check` for any registry, and `Compare`, which runs one script straight into the registry and through MCP and REST, and reports differences. |

## Using them

```sh
go get github.com/gerbillinae/go-tool-layers@v0.1.0
```

Register each command once, with its options and result types:

```go
r := tool.New("my-tool", version, "What it does")
r.Add(
    tool.Cmd("list-cards", "List the cards in decks", func(ctx context.Context, cx tool.Context, sel Selection) (*ListResult, error) {
        return svc.List(ctx, sel)
    }).HTTP(tool.GET("/api/cards")),
    tool.Cmd("add-cards", "Add cards", addCards).Write().Only(tool.MCP, tool.REST),
)
```

Options are plain structs. The `json` and `jsonschema` tags give the MCP schema and the help; the CLI reads a few more:

```go
type Selection struct {
    Decks []string `json:"decks" flag:"deck" jsonschema:"Decks to select from."`  // no omitempty: required
    Limit int      `json:"limit,omitempty" flag:"limit" jsonschema:"At most this many."`
    File  string   `json:"file" flag:"file" file:"true" interfaces:"cli" jsonschema:"A cards file."` // the CLI reads it
}
```

Then the interfaces:

```go
root := cli.Root(cli.Options{Registry: r, Customs: []*cobra.Command{mcpCommand, serveCommand}})
server := toolmcp.NewServer(toolmcp.Options{Registry: r, Shared: changeLog})
site := &rest.Site{Handler: func(url, token string) http.Handler {
    return rest.Handler(rest.Options{Registry: r, Token: token, Files: webFiles})
}}
```

Field tags: `flag`, `short`, `file:"true"` (a path the CLI reads, or `-` for stdin), `yes:"true"` (set by `--yes`), and `interfaces:"mcp,rest"` (offered only there; left out of the other interfaces' schemas, flags and required checks).

## Status

Extracted from anki-cards, where every package is used and tested, including a conformance test across the interfaces. The API is young and may change.
