# Retrieval suites

`evals/<repo>/eval.yaml` holds symbol-less questions (they describe behavior and name no function). `eval.symbols.yaml` holds shorter, differently worded questions about one concept each; most do not name an identifier either, so on Laravel and Kubernetes it is a second, harder paraphrase suite. `eval.identifiers.yaml` holds 30 generated questions per repo that do name one function ("where is X defined and what does it do"), scored by symbol: it checks that an exact name is found, and sits at 1.00 on every repo. Each case expects a file.

Suites exist for open-source checkouts only: Cobra, Hugo, Laravel framework, Kubernetes `pkg/` and `cmd/`, and this repository. A private monorepo's suite is not kept here.

Index the checkout with the embedder you use, then run:

    bench/recall.sh cobra /path/to/cobra

`--lexical` leaves the embedder out, `--focus` ranks the way `onfly_focus` does.
