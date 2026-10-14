# Parsers

A parser turns one source file into a JSON array of chunks. Onfly loads it as WASM (WASI) or as a subprocess. The core has no PHP, JavaScript, or Go grammar of its own.

The router picks a parser by file suffix. Matching is case-insensitive. The longest suffix wins, so `.blade.php` is chosen over `.php` when both are configured. The same extension on two parsers is a configuration error. A file whose suffix has no parser is not opened.

Every configured parser is opened when indexing starts. A missing WASM file fails the run even if no file uses that extension.

## Chunk JSON

Stdout is a JSON array. An empty file is `[]`. `null` is treated as an empty array. Anything else that is not a JSON array is an error. More than 32 MiB of stdout fails that file and leaves its previous chunks in place.

```json
[
  {
    "file_path": "src/svc.go",
    "type": "method",
    "content": "func (s *Service) Execute() {}",
    "metadata": {
      "name": "Execute",
      "receiver": "Service"
    }
  }
]
```

| Field | Meaning |
| --- | --- |
| `file_path` | Path the host passed in. If the plugin leaves it empty, the host fills it. |
| `type` | Kind of chunk, such as `function`, `method`, `struct`, `interface`, `class`. |
| `content` | Text that is embedded and returned by search. |
| `metadata` | String map. `name` is required for `get_component_ast`; `onfly doctor` counts chunks without it. |

The Go parser ([OnFlyApp/ext-parser-go](https://github.com/OnFlyApp/ext-parser-go)) emits `function`, `method`, `struct`, `interface`, and `type`. Methods set `metadata.receiver`. `onfly ext install go` installs a published release.

## WASM

Build a `wasip1` command, not a reactor with custom `alloc` and `parse` exports:

```sh
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o plugin.wasm .
```

The host runs the module with wazero and `wasi_snapshot_preview1`.

- `argv[0]` is `plugin`.
- `argv[1]` is the file path. A missing path should exit 1 and write the reason to stderr.
- Stdin is the file bytes.
- Stdout is the JSON array.
- Stderr is diagnostics.

Exit 0 is success. A non-zero exit fails that file. The error includes the last 4 KiB of stderr.

## Install

`onfly ext install go` downloads the published Go parser from `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json`, checks its SHA-256, and appends a wasm parsers entry. `onfly ext install go@1.0.0` pins that version. `ONFLY_REGISTRY_URL` replaces the registry URL. The lockfile is `.onfly/plugins/installed.json`.

`onfly ext install ./parser.wasm --extensions=".twig,.html"` copies a local file and does not contact the registry.

If another parser already owns one of the new extensions, a terminal asks `Replace parser for .php, .inc? [y/N] `. `--yes` overwrites. Enter or `n` returns `ext: install cancelled` and leaves the project unchanged. Without a terminal, the command returns `parser for ".php, .inc" already exists; rerun with --yes` and writes nothing. The same wasm path is updated in place.

`onfly ext update` upgrades registry plugins. `onfly ext remove go` deletes that wasm file and its config entry. A lock file name that contains a slash or `..` is refused, and no file is deleted. `onfly ext list` prints `active`, `outdated`, or `corrupted`. The registry fetch stops after three seconds. If it fails, the table still prints and a `registry:` line follows it. A hash mismatch is `corrupted`.

To build the Go parser from source:

```sh
git clone https://github.com/OnFlyApp/ext-parser-go.git
cd ext-parser-go
make wasm
```

From the project that contains `.onfly/config.yaml`:

```sh
onfly ext install /path/to/ext-parser-go/bin/go-parser.wasm --extensions=".go"
```

That copies the file into `.onfly/plugins/` and appends the wasm parser entry. `onfly init` leaves `parsers` empty. Indexing opens every configured parser.

## Stdio

`type: stdio` runs the configured command through the shell, so a cloned project could name any command. It is refused unless the environment variable `ONFLY_ALLOW_STDIO=1` is set. WASM parsers are sandboxed and need no opt-in. On Unix the host executes:

```sh
sh -c '<command> "$@"' sh <file path>
```

The file path is `$1`. Stdin is the file bytes. Stdout and stderr follow the same rules as WASM. On Windows the host runs `cmd /c <command> <file path>`.

A Markdown or Blade chunker can be a PHP script, a shell script, or the native Go parser binary (`make native` in that repo). The command is not prefixed with the project root. Use a path relative to the working directory, or an absolute path.

## Text

`type: text` is a built-in fallback for files with no language parser. It needs no plugin file:

```yaml
indexer:
  parsers:
    - type: text
      extensions: [".md", ".yaml"]
```

Markdown (`.md`, `.markdown`, `.mdx`) is split on ATX headings (`#` to `######` followed by a space; headings inside code fences are ignored). Each section is a `section` chunk named `<file>#<heading>`, for example `README.md#Install`. Text before the first heading is named `<file>`. Every other extension is split into runs of blank-line-separated paragraphs of at least 30 lines, as `block` chunks named `<file>:<start line>`, for example `conf.yaml:1`. Both kinds carry `start_line` and `end_line`. Names use the base file name, so two `README.md` files do not share a bare heading name.

The `include` globs must also cover these files, or they are never indexed.
