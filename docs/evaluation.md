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

## Large repositories and other languages

Retrieval only, without an agent: for each question, `onfly_focus` with `limit: 5` against a simulated keyword search (rank files by the question's words, read the top three whole). A hit is the expected file in the top five. Token counts are bytes divided by four. The indexes were built without an embedder unless noted. Questions for the last three rows were written as paraphrases that do not repeat the code's names, which favors search by meaning.

| Repository | Files / chunks | Index time | `onfly_focus` | Keyword search | Tokens, focus vs keyword | Median latency |
| --- | --- | --- | --- | --- | --- | --- |
| cobra (Go) | 40 / 653 | 1.7 s | 15/16 | 16/16 | 20k vs 498k | 40 ms |
| this repository (Go) | 190 / 1.4k | 1 s | 12/20 | 5/20 | 21k vs 162k | 50 ms |
| Hugo (Go and docs) | 2,029 / 12k | 13 s | 12/18 (4/18 before rarity weighting, code-only candidates and file coverage) | 8/18 | 20k vs 129k | 146 ms |
| Laravel (PHP), no embedder | 3,208 / 56k | 5 min | 4/15 | 5/15 (8/15 without tests) | 18k vs 978k | 165 ms |
| Laravel, with embeddings | same | 85 min | 6/15 | same | 13k vs 978k | 273 ms |
| Kubernetes (Go), no embedder | 4,685 / 44k | 44 s (746 s before the split fix) | 2/11 | 0/11 | 15k vs 5.9M | 540 ms |

`onfly_xray` used 40 to 60% of the tokens of reading the files it covers (cobra 41%, Hugo 57%, this repository 53%), and a file with no function bodies, such as an interface, costs about the same as the file. On the five largest Laravel files it answers in 100 to 220 ms, on Kubernetes' `validation.go` (373 chunks) in 0.4 s.

Two agents, same questions, one with `Grep` and `Read` and one with the Onfly tools: on this repository (10 questions) and on Laravel (8 questions: routing middleware, container resolution, `Model::save`, callers of one method, Blade, queue, filesystem, validation) every answer was correct in both arms, at about the same tokens (50k each), 33 to 48 seconds. Onfly's answers were no better, and on Laravel two were less complete; its line numbers for PHP were off by the docblock length before the declaration-line fix. On a well-named codebase, direct questions are answered by a capable agent with grep. The gain is in tokens per lookup, in reading large files, and in questions about who calls what.

What the larger repositories exposed, all fixed: the split overlap tokenized the whole chunk once per rune it dropped (59% of the CPU on Kubernetes); the resolvers recompiled a regular expression per candidate (5 s for `Model.php`); "who references this name" scanned every chunk; document frequencies of common words were counted in full; 17% of the Kubernetes chunks were generated files; a PHP `$this->getName()` resolved to unrelated classes; and Markdown sections appeared as callers. Still open: the PHP parser's 80 KB limit and per-file cost ([Parsers](parsers.md#known-limits-of-the-published-parsers)).

