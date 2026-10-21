# Parsers

A parser turns one source file into a JSON array of chunks. Onfly loads it as WASM (WASI) or as a subprocess. The core has no grammar of its own.

The router picks a parser by file suffix. Matching is case-insensitive. The longest suffix wins, so `.blade.php` is chosen over `.php` when both are configured. The same extension on two parsers is a configuration error, unless every parser that claims it has an `include` scope; see [per-parser scope](configuration.md#per-parser-scope). A file whose suffix has no parser is not opened.

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
| `metadata` | String map; the keys are listed [below](#metadata-keys). `name` is required for symbol lookup (`onfly_xray` with a symbol, `get_component_ast`) and for the blast radius; `onfly doctor` counts chunks without it. |

### Metadata keys

Every key is optional except `name` on a chunk that is a symbol. The host reads the ones in the table; a parser may add others, which are stored and shown but not interpreted. All values are strings.

| Key | Set by | Meaning |
| --- | --- | --- |
| `name` | all | The symbol's own name (`Execute`, `ClawBackHandler`). A repeated name in one file is allowed. |
| `receiver` | methods, members | The type that owns the chunk: `Service` for Go, the qualified class for PHP. Call resolution uses it to tell same-named methods apart. |
| `namespace` | PHP | The namespace the chunk is in. A call resolves to a class of this namespace or of an import. |
| `fqn` | PHP | The qualified name: `App\Domain\Cycle` for a class, `App\Domain\Cycle::end` for a method. |
| `package` | Go | The package clause of the file. |
| `signature`, `params`, `return_type` | PHP | The declaration line, the parameter list as written, the declared return type. `onfly_xray` shows `signature`. |
| `start_line`, `end_line` | optional | 1-based. When left out the host finds the chunk's first match in the file. |
| `part` | split chunks | `2/3`: the chunk is the second of three parts of a long body. Parts are joined in order. |
| `param_types` | function, method | The classes the parameters are typed with, in order, comma separated, without repeats. PHP: fully qualified through the file's `use` imports and namespace, scalars left out. Go: `Type` for the same package, `import/path.Type` for another. |
| `type_fqn` | PHP property | The qualified classes of the declared type. |
| `handles` | function, method | The one class a handler or listener entry handles. Set it when the name is a handler entry (`__invoke`, `handle`, `execute`, `handle*`, `on*`; Go `Handle`, `Execute`, `Handle*`, `On*`) and the first parameter (after a `context.Context` in Go) is typed with one class. Qualified like `param_types`. |

The host does not parse type syntax itself. A parser that wants a language's dispatch pattern understood qualifies the type names and sets `handles`; the host then lists the code that builds that type as a caller of the handler in `onfly_xray`'s `called_by` and `onfly_focus`'s call graph, since a command bus, not a call, joins them. Without these keys the call graph still works from names, `receiver` and the imports it reads from the source.

Imports are not chunks. The PHP parser reads `use` statements to qualify type names; the host reads the same lines from the source file when it resolves a call, so a parser does not have to pass them on.

The Go parser ([OnFlyApp/ext-parser-go](https://github.com/OnFlyApp/ext-parser-go)) emits `function`, `method`, `struct`, `interface`, and `type`. Methods set `metadata.receiver`; functions and methods also set `package`, `param_types` and `handles`. `onfly ext install go` installs a published release.

## Known limits of the published parsers

- PHP ([OnFlyApp/ext-parser-php](https://github.com/OnFlyApp/ext-parser-php)): a file over 80 KB (`maxParseSize`) is not parsed into symbols; it comes back as plain `file` chunks with `reason: too_large`, so `onfly_xray` has no outline or blast radius for it. In Laravel that is about twenty files, among them `Eloquent/Model.php` and `Query/Builder.php`. Parsing costs about 0.6 s of CPU per file, against about 10 ms for the Go parser, so a 3,000-file project takes minutes. A chunk's `start_line` is the line of its docblock; `onfly_xray` and `onfly_focus` show the declaration line.
- Go: a chunk includes the doc comment above a declaration, for the same reason.

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

### Batch mode

A plugin that starts the Go runtime (or a grammar) on every run pays for it on every file: about half a second for a tree-sitter grammar. A plugin may instead keep one process alive. The host looks for the bytes `onfly-batch/1` in the module and, when they are there, starts it with `argv = ["plugin", "--batch"]`:

1. The plugin prints `{"ready":"onfly-batch/1"}` and a newline.
2. For each line of stdin, `{"path": "src/a.php", "content": "<base64 of the file>"}`, it prints one line: `{"chunks": [...]}` or `{"error": "..."}`.
3. It exits when stdin ends.

The host runs up to one instance per core, replaces an instance after 2,000 files, and falls back to a one-shot run for a file whose instance died. A plugin without the marker is run once per file as above; both forms must produce the same chunks.

### Writing your own

1. Print the [chunk JSON](#chunk-json); set `name` on symbols, and `receiver`, `namespace`/`fqn` or `package` where the language has them.
2. Qualify type names and set `param_types` and `handles` if the language has typed parameters and a handler convention (see [metadata keys](#metadata-keys)).
3. Build a `wasip1` command, with no CGO and a pinned toolchain so the bytes are reproducible: `CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o plugin.wasm .`
4. Point a project at it without publishing anything:

```yaml
indexer:
  parsers:
    - extensions: [.twig]
      wasm_path: ./plugins/twig.wasm     # same as type: wasm and path:
      exclude: ["**/vendor/**"]
      ignore_directives: []              # see "Language rules" in configuration.md
```

   `onfly doctor` checks that the file exists and is a WebAssembly module, and prints its SHA-256.
5. To publish it, tag a release in an `ext-parser-<name>` repository and add it to the [registry](https://github.com/OnFlyApp/registry/blob/main/docs/publishing.md).

## Install

`onfly ext install go` downloads the published Go parser from `https://raw.githubusercontent.com/OnFlyApp/registry/main/registry.json`, checks its SHA-256, and appends a wasm parsers entry. `onfly ext install go@1.0.0` pins that version. `ONFLY_REGISTRY_URL` replaces the registry URL. The lockfile is `.onfly/plugins/installed.json`.

`onfly ext install ./parser.wasm --extensions=".twig,.html"` copies a local file and does not contact the registry.

If another parser already owns one of the new extensions, a terminal asks `Replace parser for .php, .inc? [y/N] `. `--yes` overwrites. Enter or `n` returns `ext: install cancelled` and leaves the project unchanged. Without a terminal, the command returns `parser for ".php, .inc" already exists; rerun with --yes` and writes nothing. The same wasm path is updated in place.

`onfly ext update` upgrades registry plugins. `onfly ext remove go` deletes that wasm file and its config entry. A lock file name that contains a slash or `..` is refused, and no file is deleted. `onfly ext list` prints `active`, `outdated`, or `corrupted`. The registry fetch stops after three seconds. If it fails, the table still prints and a `registry:` line follows it. A hash mismatch is `corrupted`.

Official parsers: `go`, `php`, `typescript` (`.ts`, `.tsx`, `.mts`, `.cts`), `javascript` (`.js`, `.jsx`, `.mjs`, `.cjs`), `python` (`.py`, `.pyi`) and `rust` (`.rs`). Each lives in its own `ext-parser-<name>` repository.

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

That copies the file into `.onfly/plugins/` and appends the wasm parser entry. `onfly init` configures only the built-in `text` parser. Indexing opens every configured parser.

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

Markdown (`.md`, `.markdown`, `.mdx`) is split on ATX headings (`#` to `######` followed by a space; headings inside code fences are ignored; a fence closes only on the same character and at least the opening length). YAML front matter (first line `---` up to the next `---` or `...`) belongs to the first section and is not scanned for headings. A repeated heading in one file gets `~2`, `~3`, ... appended, so `CHANGELOG.md#Added`, `CHANGELOG.md#Added~2`. Each section is a `section` chunk named `<file>#<heading>`, for example `README.md#Install`. Text before the first heading is named `<file>`. Every other extension is split into runs of blank-line-separated paragraphs of at least 30 lines, as `block` chunks named `<file>:<start line>`, for example `conf.yaml:1`. Both kinds carry `start_line` and `end_line`. Names use the base file name, so two `README.md` files do not share a bare heading name.

The `include` globs must also cover these files, or they are never indexed.
