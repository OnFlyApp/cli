# Retrieval suites

`evals/<repo>/eval.yaml` holds symbol-less questions (they describe behavior and name no function). `eval.symbols.yaml` holds shorter, differently worded questions about one concept each; most do not name an identifier either, so on Laravel and Kubernetes it is a second, harder paraphrase suite. `eval.identifiers.yaml` holds 30 generated questions per repo that do name one function ("where is X defined and what does it do"), scored by symbol: it checks that an exact name is found, and sits at 1.00 on every repo. Each case expects a file.

Suites exist for open-source checkouts only: Cobra, Hugo, Laravel framework, Kubernetes `pkg/` and `cmd/`, and this repository. A private monorepo's suite is not kept here.

Index the checkout with the embedder you use, then run:

    bench/recall.sh cobra /path/to/cobra

`--lexical` leaves the embedder out, `--focus` ranks the way `onfly_focus` does.

`recall.sh` prints the file-level recall@5 of both file suites in four modes (plain and `--focus`, lexical and hybrid), then the symbol-level recall@5 of the identifier suite with `--focus`. It copies the suites into the checkout's `.onfly/`.

## Numbers at the last run

`onfly_focus` ranking, hybrid index (`qwen3-embedding:0.6b`), file-level recall@5. Kubernetes and Laravel need hours to embed, so keep their indexes.

| Repository | `eval.yaml` | `eval.symbols.yaml` | `eval.identifiers.yaml` |
| --- | --- | --- | --- |
| cobra | 1.00 | 0.94 | 1.00 |
| hugo | 0.57 | 0.72 | 1.00 |
| onfly | 0.60 | 0.90 | 1.00 |
| laravel | 0.50 | 0.20 | 1.00 |
| kubernetes | 0.50 | 0.36 | 1.00 |

A change to ranking, chunking or the embedded text should leave these as they are or raise them; a lower number on a suite you did not aim at is a regression. [Evaluation](../docs/evaluation.md#benchmarks) has the history and the agent comparison.
