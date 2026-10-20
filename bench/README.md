# Retrieval suites

`evals/<repo>/eval.yaml` holds symbol-less questions (they describe behavior and name no function); `eval.symbols.yaml` holds the older questions that name the symbol. Each case expects a file.

Suites exist for open-source checkouts only: Cobra, Hugo, Laravel framework, Kubernetes `pkg/` and `cmd/`, and this repository. A private monorepo's suite is not kept here.

Index the checkout with the embedder you use, then run:

    bench/recall.sh cobra /path/to/cobra

`--lexical` leaves the embedder out, `--focus` ranks the way `onfly_focus` does.
