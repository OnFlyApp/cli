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
onfly eval [--cases FILE[,FILE...]] [--k 10] [--split even|odd] [--file-level] [--min-recall 0] [--baseline FILE [--update-baseline]] [--json]
onfly eval tune [--cases FILE[,FILE...]] [--k 10] [--file-level] [--top 5]
```

`--cases` takes several files, repeated or comma-separated; they are scored as one list. `--split even` or `odd` scores every other case, the halves `tune` fits and checks on. `--file-level` counts a hit when the file matches, whatever the symbol, which is the fair measure when a file holds near-duplicate chunks.

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

## Real questions

Generated cases are written from the code, so they favour retrieval. Real questions are better. Set `server.usage_file` (see [configuration](configuration.md#call-graph-and-usage-lists)), work with your assistant for a while, then:

```
onfly eval mine [--usage FILE] [--out .onfly/eval-mined.yaml]
```

Each recorded question becomes a case whose expected hit is the chunk the assistant opened next (up to three per question). The result is an ordinary cases file: pass it to `onfly eval --cases` or `onfly eval tune` beside your hand-written one. A question the assistant answered from the first hit gives an easy case, and one it opened the third hit for gives a hard one, so the file shows where ranking falls short.

## Tuning

`onfly eval tune` finds fusion weights for your code. It retrieves every case once, then tries a grid of `semantic_weight`, `keyword_weight`, `word_weight`, `field_weight` and `rrf_k` by re-fusing the saved lists, so the whole grid takes seconds after the retrieval. It picks the best setting by MRR on the even-numbered cases, scores that setting and your current one on the odd-numbered cases, and prints the YAML to adopt only when the odd half gains at least 0.01 MRR. Use at least a few hundred cases; with 20 the check half is noise.

Large case lists can be generated: ask a local model for one question per sampled symbol, drop questions that contain the symbol name, and keep the symbol as the expectation. Such cases flatter retrieval, because the question was written from the code, so keep your hand-written cases as a separate file and look at both.

## Measured effect

Measured on this repository with `qwen3-embedding:0.6b`, 573 cases (45 hand-written, 528 generated), k=10, symbol-level hits:

| Change | R@1 | R@5 | R@10 | MRR |
| --- | --- | --- | --- | --- |
| Before: semantic 1, keyword 1, rrf_k 60 | 0.45 | 0.83 | 0.93 | 0.608 |
| Name list at weight 1 | 0.43 | 0.81 | 0.90 | 0.589 |
| Tuned: semantic 2, keyword 0.5, field 0.5, rrf_k 20 | 0.51 | 0.86 | 0.94 | 0.663 |
| Tuned + `dedupe` | 0.51 | 0.86 | 0.94 | 0.663 |
| Tuned + `group_by: file` (symbol-level) | 0.50 | 0.79 | 0.82 | 0.622 |
| Tuned + `search.expand` (qwen2.5-coder:7b) | 0.54 | 0.87 | 0.94 | 0.679 |
| Tuned + `indexer.summaries` (qwen2.5-coder:7b) | 0.64 | 0.92 | 0.96 | 0.751 |
| Tuned, file-level hits | 0.60 | 0.91 | 0.97 | 0.736 |
| Tuned + `group_by: file`, file-level hits | 0.60 | 0.94 | 0.98 | 0.740 |
| Tuned + summaries, file-level hits | 0.71 | 0.94 | 0.98 | 0.807 |

`group_by: file` pays off only when the question is about a file; a question about a symbol loses its second candidate in the same file. It is off by default.

Summaries are the largest single gain (MRR 0.663 to 0.751; on the 45 hand-written cases alone 0.522 to 0.639) and cost one model call per chunk at index time: 16 minutes for 733 chunks with a 7B model on a laptop, then only changed chunks. Query expansion gains about 0.016 MRR and adds roughly 3 seconds to every new query, so it stays opt-in.

Second round, on an index with summaries (738 to 845 chunks, same cases):

| Change | R@1 | R@5 | R@10 | MRR |
| --- | --- | --- | --- | --- |
| Summaries, one call per chunk | 0.63 | 0.92 | 0.95 | 0.750 |
| Summaries, `batch: 4` | 0.61 | 0.88 | 0.94 | 0.722 |
| `batch: 4` + `embed_neighbours` | 0.60 | 0.91 | 0.95 | 0.732 |
| `batch: 4` + `files: true` | 0.60 | 0.87 | 0.93 | 0.717 |
| `batch: 4` with `qwen2.5-coder:3b` | 0.60 | 0.89 | 0.95 | 0.718 |
| One call per chunk + `graph_weight: 0.25` | 0.60 | 0.92 | 0.95 | 0.731 |

With summaries on, `onfly eval tune` found no setting that beat the defaults by 0.01 MRR on the check half.
