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
onfly eval mine [--usage FILE] [--out FILE]
```

`--explain` lists every case whose expected chunk was not first, with the top three results, and counts what took rank 1: another file's code, another symbol of the right file, a docs section over code, or code over a docs section. Read it before changing anything: it says which kind of mistake dominates.

`--judge MODEL` asks a local chat model (Ollama; `--judge-endpoint` to change the URL) whether any result ranked above the expected one also answers the question, and prints judged recall and MRR beside the strict ones. A single expected chunk understates quality, because a different chunk can be a correct answer. The judge is a model, so treat its numbers as an upper bound and compare them only with each other.

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

Generated cases are written from the code, so they favour retrieval. Real questions are better. Set `server.usage_file` (see [configuration](configuration.md#usage-list)), work with your assistant for a while, then:

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

## Third round: prompts, second embedder, summary variants

Same 573 cases, embeddinggemma-2 with `qwen2.5-coder:7b` summaries, fresh index each time (the summary cache made them take one to two minutes), symbol-level:

| Change | R@1 | R@5 | R@10 | MRR | human MRR |
| --- | --- | --- | --- | --- | --- |
| Baseline (no prompts, one embedder) | 0.698 | 0.935 | 0.969 | 0.797 | 0.705 |
| Summary keyword list switched off | 0.689 | 0.932 | 0.970 | 0.794 | 0.699 |
| Code-retrieval prompts | 0.712 | 0.934 | 0.963 | 0.810 | 0.774 |
| "Search result" prompt | 0.703 | 0.949 | 0.972 | 0.806 | 0.711 |
| Second embedder (qwen3-embedding:0.6b) | 0.716 | 0.928 | 0.972 | 0.809 | 0.737 |
| Prompts + second embedder | **0.726** | **0.941** | 0.974 | **0.816** | 0.767 |
| Prompts + second embedder with a Qwen instruct prefix | 0.714 | 0.939 | 0.974 | 0.811 | 0.759 |
| Summary vector (removed) | 0.702 | 0.908 | 0.955 | 0.789 | 0.717 |
| Three questions per chunk (removed) | 0.686 | 0.921 | 0.967 | 0.788 | 0.644 |

With `--explain`, 157 of 573 cases were not first: 85 behind another file's code, 33 behind another symbol of the right file, 23 behind a docs section, 16 the other way round. With `--judge qwen2.5-coder:7b`, 130 of those had a better-ranked result judged to answer the question, which lifts MRR from 0.812 to 0.947; most of the "misses" are valid answers that differ from the single expected chunk.

## Embedding models

Six models, summaries off, search defaults, same cases (index time from scratch):

| Model | Dims | Index | R@1 | R@5 | R@10 | MRR | human MRR |
| --- | --- | --- | --- | --- | --- | --- | --- |
| embeddinggemma-2 | 768 | 32 s | 0.618 | 0.918 | 0.955 | **0.745** | 0.704 |
| embeddinggemma | 768 | 21 s | 0.590 | 0.894 | 0.946 | 0.716 | 0.603 |
| qwen3-embedding:0.6b | 1024 | 49 s | 0.511 | 0.859 | 0.932 | 0.658 | 0.520 |
| bge-m3 | 1024 | 38 s | 0.518 | 0.834 | 0.899 | 0.649 | 0.680 |
| nomic-embed-text | 768 | 20 s | 0.361 | 0.651 | 0.759 | 0.484 | 0.410 |
| mxbai-embed-large | 1024 | 34 s | 0.251 | 0.475 | 0.546 | 0.345 | 0.332 |

The model matters more than anything else in the pipeline short of summaries. Run your own comparison with a config per model and `onfly eval`.

## Removed options

These were options in earlier builds. Each was measured above and none improved on the final configuration, so the code was deleted: `search.expand` (query expansion: +0.016 MRR for 3 s per new query), `search.group_by` and `search.dedupe` (no gain or a loss at symbol level), `search.graph_weight` (helped hand-written cases, hurt generated ones; `eval tune` never picked it), `indexer.summaries.batch` (28% faster, -0.03 MRR), `indexer.summaries.questions` and `indexer.summary_vector` (-0.009 MRR, 43 minutes of model time) and `indexer.embed_neighbours` (+0.010 MRR only with batching, nothing without). The tables above keep their rows for the record.

## A PHP project

A Laravel and Livewire application (107 files, 632 chunks, embeddinggemma-2, no summaries), 100 hand-written cases, file-level hits: recall@10 1.00, MRR 0.838; 73 cases ranked first. The misses put a neighbouring file first. The chunks that outranked the right file were small ones: `trait_use`, `enum_case` and `property`. They are the reason for `search.type_weights`, which demotes a type without removing it; `indexer.skip_types` is for chunks, such as `use` and `namespace`, that have no search value at all. `onfly stats` lists the types an index holds.

Dropping types with `indexer.skip_types`, same 100 cases, recall@10 1.00 throughout:

| `skip_types` | Chunks | MRR |
| --- | --- | --- |
| `use`, `namespace` | 632 | 0.838 |
| + `trait_use` | 611 | 0.845 |
| + `property` | 559 | 0.849 |
| + `enum_case` | 532 | 0.885 |

None of the 100 cases asks for a single enum case or property value, so the table cannot show what dropping them costs: a question like "which approval status exists" now lands on the enum or class chunk. Add such cases before dropping `property` or `enum_case`, or use `type_weights` to demote them instead.

Demoting the same types with `search.type_weights` instead of deleting them (632 chunks, `use` and `namespace` still skipped):

| Setting | MRR | Notes |
| --- | --- | --- |
| Weights 0.3 / 0.6 / 0.7, final score only | 0.851 | lowering them further changed nothing |
| Weights 0.05 / 0.1 / 0.1, fractional list positions, 3x candidate pool | 0.868 | "five million" lookup ranks 5 |
| `skip_types` for `trait_use`, `property`, `enum_case` | 0.885 | those chunks cannot be found |

The first rows were stuck because demoted chunks still filled the top 20 slots of every candidate list; reading a deeper pool and letting a demoted chunk take a fraction of a position is what moved the score. The remaining 0.017 is the price of keeping enum and property lookups.


## A Go project, end to end

Agent benchmark on Hugo (523 non-test Go files, 6,967 chunks, embeddinggemma-2, no summaries): seven tasks, three runs each, `claude -p` with and without the MCP server, sum of per-task medians.

| Arm | Turns | Seconds | Output tokens |
| --- | --- | --- | --- |
| Without onfly | 37 | 72 | 8,092 |
| With onfly, server instructions only | 33 | 64 | 7,082 |
| With onfly, instructions + `onfly guide --write` CLAUDE.md block | 37 | 64 | 6,773 |

The gain comes from the semantic questions (how is X chosen, how is Y decided: 4 to 3 and 7 to 4 turns). On an exact lookup, Grep is already one call, so onfly adds none. With the CLAUDE.md block the model called `search` 15 times instead of never, and the turn count rose back to the baseline: more onfly use did not mean fewer turns on a repository the model already knows well. All answers were equally correct and the edit task built and passed `go test` in every run.

`graph` returned 7,219 tokens for one callers query before it defaulted to signatures; it now returns 544 for the same nine hits (`detail: full` keeps bodies).
