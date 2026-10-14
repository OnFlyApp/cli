# Evaluation

`onfly eval` scores search results against expectations you write by hand, so you can tell whether a change to chunking, ranking, or config helped.

## Case file

Default path: `.onfly/eval.yaml` (relative to the project root).

```yaml
cases:
  - query: where is the content hash computed
    expect:
      - file: internal/index/index.go
        symbol: ContentHash
```

A case hits at rank r when a returned chunk has a path ending in `expect.file` and, if `symbol` is set, `metadata.name` equals it. Every case needs a query and at least one expectation with a file.

## Metrics

- **recall@k**: share of cases with a hit in the top k results.
- **MRR**: mean of `1/rank` of the first hit per case; a miss counts as 0.

## Usage

```
onfly eval [--cases .onfly/eval.yaml] [--k 10] [--min-recall 0] [--baseline FILE [--update-baseline]] [--json]
```

`--min-recall` makes the command exit non-zero when recall falls below the value, for CI.

`--baseline FILE` compares recall and mrr to a saved run and exits non-zero on any drop; the `k` must match. Record it with `--update-baseline` and commit the file.

Write 20 to 50 real questions you would ask about your code. Fewer is noisy; synthetic ones mislead.

## Before and after

```
onfly eval --json > before.json
# change a setting, reindex
onfly eval --json > after.json
```

Compare `recall` and `mrr`, then look at the per-case `rank` for what moved.
