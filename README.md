# Onfly

Onfly indexes a codebase and serves that index to an IDE over the Model Context Protocol (MCP) on stdin and stdout.

Parsers are separate programs. A WASM module or a shell command reads one source file and returns JSON chunks. Onfly embeds those chunks and stores them in SQLite (`sqlite-vec` and FTS5) or Postgres (`pgvector`). `onfly serve` answers search and file tools. It does not write logs to stdout.

Operator guides are under [docs/](docs/). Source: <https://github.com/OnFlyApp/cli>.

## Requirements

- Go 1.27 or newer
- An embedder: [Ollama](https://ollama.com) running locally, an OpenAI API key, or a Voyage API key
- A parser for each extension you index. This repo does not ship language parsers. The Go parser is [OnFlyApp/ext-parser-go](https://github.com/OnFlyApp/ext-parser-go). `onfly ext install go` installs a published release.

## Build

```sh
make build
```

The binary is `bin/onfly`. The build sets `CGO_ENABLED=0`.

## Index a Go project

Install a 1024-dimension model if you use the sample settings:

```sh
ollama pull qwen3-embedding:0.6b
```

In the target project:

```sh
onfly init
onfly ext install go
```

`onfly ext install go` downloads `go-parser.wasm` from [OnFlyApp/registry](https://github.com/OnFlyApp/registry), checks its SHA-256, and adds a `.go` parser. To build that parser yourself instead:

```sh
git clone https://github.com/OnFlyApp/ext-parser-go.git
cd ext-parser-go
make wasm
```

`make wasm` writes `bin/go-parser.wasm`. From the project that contains `.onfly/config.yaml`:

```sh
onfly ext install /path/to/ext-parser-go/bin/go-parser.wasm --extensions=".go"
```

`onfly init` writes an empty `parsers` list. An existing config file is left in place. `onfly ext install go` adds the Go parser. A project that should index only Go can use:

```yaml
project: demo

database:
  driver: sqlite
  dsn: .onfly/vector.db

embedder:
  provider: ollama
  endpoint: http://localhost:11434
  model: qwen3-embedding:0.6b
  dimensions: 1024

rules:
  - .onfly/rules/architecture.md

indexer:
  include: ["**/*.go"]
  exclude: ["vendor"]
  parsers:
    - extensions: [".go"]
      type: wasm
      path: .onfly/plugins/go-parser.wasm

server:
  transport: stdio
  log_level: info
  log_file: .onfly/server.log
```

Then:

```sh
onfly index
onfly serve
```

`onfly index` prints a one-line summary on stderr. It exits 1 if any file failed to parse. An unchanged file is not parsed again. `onfly serve` speaks JSON-RPC on stdout. Logs go to `.onfly/server.log`.

Point the IDE at the project directory, or pass `--config` with the absolute path of `config.yaml`. The client config is in [docs/mcp.md](docs/mcp.md).

## Commands

| Command | What it does |
| --- | --- |
| `onfly init` | Create `.onfly/`, a sample config, and three rule files. |
| `onfly init --force` | Overwrite that config and those rule files. |
| `onfly init --ci` | Also write `.github/workflows/onfly-index.yml`. An existing config is left in place. `--force` overwrites the workflow. |
| `onfly index` | Parse matching files, embed the chunks, and update the database. |
| `onfly index --dry-run` | Parse and print a token and cost estimate. Does not open the database or call the embedder. |
| `onfly index --ci` | Embed 128 chunks per request. The default batch is 32. |
| `onfly watch` | Reindex changed files after `indexer.debounce_ms`. |
| `onfly serve` | Serve the MCP tools on stdio. |
| `onfly serve --max-tokens N` | Cap search results at N tokens. `N` of 0 keeps `server.max_tokens`. |
| `onfly doctor` | Check config, the database, the embedder, and parser files. |
| `onfly optimize` | Compact the SQLite FTS index or reindex the Postgres indexes. |
| `onfly ext install go` | Download the published Go parser, check SHA-256, and add it to the config. |
| `onfly ext install go@1.0.0` | Install that published version. |
| `onfly ext install ./parser.wasm --extensions=".twig,.html"` | Install a local wasm file without the registry. |
| `onfly ext update` | Upgrade installed registry parsers. |
| `onfly ext remove go` | Remove the wasm file and its config entry. |
| `onfly ext list` | Show name, version, and `active`, `outdated`, or `corrupted`. |
| `--config path` | Use this config file instead of walking parent directories. |

`onfly ext` reads `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json`, or the URL in `ONFLY_REGISTRY_URL`. A downloaded file is kept only when its SHA-256 matches the registry. A mismatch deletes the temp file and leaves the project unchanged. Installed plugins are recorded in `.onfly/plugins/installed.json`. `onfly ext list` waits at most three seconds for the registry, then prints the table. A failed fetch adds a `registry:` line.

## Guides

- [Configuration](docs/configuration.md)
- [Parsers](docs/parsers.md)
- [Indexing](docs/indexing.md)
- [MCP server](docs/mcp.md)
- [Development](docs/development.md)
