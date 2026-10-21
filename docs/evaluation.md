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

Generated cases are written from the code, so they favour retrieval. Real questions are better. Set `server.usage_file` (see [configuration](configuration.md#usage-log)), work with your assistant for a while, then:

```
onfly eval mine [--usage FILE] [--out .onfly/eval-mined.yaml]
```

Each recorded question becomes a case whose expected hit is the chunk the assistant opened next (up to three per question). The result is an ordinary cases file: pass it to `onfly eval --cases` or `onfly eval tune` beside your hand-written one. A question the assistant answered from the first hit gives an easy case, and one it opened the third hit for gives a hard one, so the file shows where ranking falls short.

## Tuning

`onfly eval tune` finds fusion weights for your code. It retrieves every case once, then tries a grid of `semantic_weight`, `keyword_weight`, `word_weight`, `field_weight` and `rrf_k` by re-fusing the saved lists, so the whole grid takes seconds after the retrieval. It picks the best setting by MRR on the even-numbered cases, scores that setting and your current one on the odd-numbered cases, and prints the YAML to adopt only when the odd half gains at least 0.01 MRR. Use at least a few hundred cases; with 20 the check half is noise.

Large case lists can be generated: ask a local model for one question per sampled symbol, drop questions that contain the symbol name, and keep the symbol as the expectation. Such cases flatter retrieval, because the question was written from the code, so keep your hand-written cases as a separate file and look at both.

## Benchmarks

Five code bases: Cobra (Go, 40 files), this repository (Go, 200 files), Hugo (Go, 2,000 files), Laravel framework (PHP, 3,200 files), Kubernetes `pkg/` and `cmd/` (Go, 4,100 files, 44,000 chunks). Embeddings are `qwen3-embedding:0.6b` through Ollama. The suites are in [`bench/evals`](../bench/README.md).

### Retrieval

File-level recall@5 of `onfly_focus` (`onfly eval --file-level --k 5 --focus`) on questions that describe behavior and name no function. Lexical is an index without an embedder. Hybrid is the default index: vectors plus the lexical lists plus file summaries.

| Repository | Lexical only | Hybrid before the retrieval work | Hybrid now |
| --- | --- | --- | --- |
| Cobra | 0.75 | 0.75 | 1.00 |
| Hugo | 0.43 | 0.29 | 0.57 |
| This repository | 0.30 | 0.40 | 0.60 |
| Laravel | 0.12 | not built | 0.50 |
| Kubernetes | 0.12 | not built | 0.50 |

"Before" was measured on the three smaller repositories only; the other three had no embeddings then. It is a lexical-weighted fusion with bare-code vectors and no instruction prefix on the Qwen3 queries; the work was embedding each chunk with its path, kind and symbol, the instruction prefix, weighing the vectors four times the lexical lists, dropping the whole-file boost next to vectors, and the file summaries. On the second suite of each repository (shorter paraphrases, `eval.symbols.yaml`) recall@5 is 0.94 on Cobra, 0.72 on Hugo, 0.90 on this repository, 0.36 on Kubernetes and 0.20 on Laravel; those questions mostly do not name an identifier either, which is why Laravel is low.

Exact names are not the problem: `eval.identifiers.yaml` asks "where is X defined and what does it do" for 30 sampled functions per repository, and symbol-level recall@5 is 150 of 150 (1.00, MRR 0.96 to 0.98). A lexical-only paraphrase question stays hard, because the words are not in the code; that gap is what the vectors close. A question whose code says `release`, `backoff` and `exception` for "tried again later" can still miss, and no ranking weight tried here fixed it.

### Agents

Two agents got the same questions, one with `Grep` and `Read` (A) and one with `onfly_xray` and `onfly_focus` (B); a third read the real source and graded every answer correct, partial or wrong. One run per arm and repository; with 8 questions a difference of one or two is noise.

| Repository | Questions | A correct | B correct | B partial / wrong | B calls (onfly + native) |
| --- | --- | --- | --- | --- | --- |
| Cobra | 8 | 7 | 7 | rest not split | 8 + 3 |
| Hugo | 8 | 6 | 6 | 2 / 0 | 25 + 3 |
| This repository | 10 | 8 | 8 | rest not split | 11 + 4 |
| Laravel | 8 | 4 | 8 | 0 / 0 | 28 + 1 |
| Kubernetes | 8 | 5 | 5 | 3 / 0 | 22 + 3 |
| Total | 42 | 30 | 34 | | |

No wrong answer was recorded on Hugo, Laravel or Kubernetes (the Cobra and this-repository runs were graded only for the number correct). Laravel is the one clear difference: grep left four answers incomplete because it did not open the body that decides the answer, and the skeleton plus `File.php:method` got the agent there. On Kubernetes the Onfly arm lost points by stopping short, not by wrong results: it did not expand the helper that held the detail. Cost: B agents used 67k to 78k tokens per repository; the A arm was not measured on the same scale, so there is no token claim here.

`onfly_xray` costs 40 to 60% of the tokens of reading the files it covers (Cobra 41%, Hugo 57%, this repository 53%), and a file with no function bodies, such as an interface, costs about the same as the file.

On a well-named codebase a capable agent answers direct questions with grep at the same cost or lower. The Onfly tools earn their place on large files (the skeleton takes about half the tokens), on who-calls-what questions, and on questions whose words are not in the code.
