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

File-level recall@5 of `onfly_focus` (`onfly eval --file-level --k 5 --focus`). Lexical is an index without an embedder; hybrid is the default index (vectors, the lexical lists, file summaries). The first suite asks questions that describe behavior and name no function; the second is a shorter, differently worded set of questions about one concept each, most of which name no identifier either.

| Repository | Lexical only | Hybrid | Hybrid, second suite | Exact names (symbol-level, 30 questions) |
| --- | --- | --- | --- | --- |
| Cobra | 0.75 | 1.00 | 1.00 | 30/30 |
| Hugo | 0.43 | 0.71 | 0.78 | 30/30 |
| This repository | 0.30 | 0.70 | 0.85 | 30/30 |
| Laravel | 0.12 | 0.62 | 0.33 | 30/30 |
| Kubernetes | 0.12 | 0.50 | 0.36 | 30/30 |

What got the hybrid column there: each chunk is embedded with its path, kind and symbol; the Qwen3 query instruction; the vector list weighing four times the lexical lists; leaving the whole-file keyword boost out next to vectors; a short summary chunk per file; and listing a second result of one file only after other files' results. Before that, the three repositories that had vectors scored 0.75, 0.29 and 0.40 on the first suite. Lexical-only indexes are unchanged by all of it.

Exact names are not the problem: `eval.identifiers.yaml` asks "where is X defined and what does it do" for 30 sampled functions per repository, and all 150 are found in the top five (MRR 0.96 to 0.98). A paraphrase question stays hard when the words are not in the code. A question whose code says `release`, `backoff` and `exception` for "tried again later" can still miss, and no ranking weight tried here fixed it.

### Agents

Two agents got the same questions on each repository, run one at a time so the timings do not share the machine: A with `Grep`, `Glob`, `Read` and read-only `Bash`, B with `onfly_xray` and `onfly_focus` (through a helper that starts `onfly serve` per call, 0.3 to 1 s) and `Grep`/`Read` as fallback, told to use Onfly for symbols, types and call or handler graphs and grep or read for env vars, string literals and flat config files. A third agent read the real source and graded every answer correct, partial or wrong. 42 questions (8 each on Cobra, Hugo, Laravel and Kubernetes, 10 on this repository), on indexes whose parsers and file summaries were current and whose vectors were not recomputed. Tokens and seconds are the harness's totals for the agent, so they include reading the prompt and the final reply.

| Repository | A correct / partial / wrong | B correct / partial / wrong | Tokens A | Tokens B | Seconds A | Seconds B | Calls A | Calls B (onfly + native) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Cobra | 8 / 0 / 0 | 6 / 2 / 0 | 47,660 | 63,419 (+33%) | 37 | 62 (+67%) | 5 | 13 + 2 |
| Hugo | 8 / 0 / 0 | 5 / 3 / 0 | 67,243 | 82,594 (+23%) | 110 | 95 (-14%) | 17 | 23 + 3 |
| This repository | 10 / 0 / 0 | 9 / 1 / 0 | 66,351 | 69,847 (+5%) | 85 | 53 (-38%) | 9 | 18 + 4 |
| Laravel | 7 / 1 / 0 | 8 / 0 / 0 | 49,907 | 66,687 (+34%) | 41 | 75 (+84%) | 5 | 33 + 0 |
| Kubernetes | 8 / 0 / 0 | 4 / 4 / 0 | 70,787 | 87,457 (+24%) | 86 | 79 (-8%) | 20 | 19 + 1 |
| Total (42 questions) | 41 / 1 / 0 | 32 / 10 / 0 | 301,948 | 370,004 (+23%) | 358 | 363 (+1%) | 56 | 106 + 10 |

Each cell is its latest run. B for Cobra and Hugo is the first run; B for this repository, Laravel and Kubernetes was run a second time after the changes below (the first run: 7/3/0, 7/1/0 and 6/2/0, at 66.9k, 68.8k and 88.7k tokens), and the graders graded the A answers of those three again (this repository 10, Laravel 8 → 7, Kubernetes 7 → 8), which shows how much a grade moves on its own. A's tokens and seconds are from its single run. The changes between B's runs: `onfly_xray` hides the licence, package line and imports above a file's first symbol behind one marker, puts a symbol's body first even in a file shown whole, and syncs the file of a bare symbol before showing its lines; `onfly_focus` lists a second result of one file after the other files' results unless the question names the function. They moved recall on the paraphrase suites (table above) and did not measurably move the agents: 21 instead of 20 correct of 26, the same tokens, the same calls.

Per question that is 7.2k tokens and 8.5 s for A and 8.8k and 8.6 s for B. The harness reports tokens and time per agent, not per question, so these are averages; the calls each agent reported per question average 1.3 for A and 2.8 for B.

**Onfly lost this comparison on accuracy and tokens.** No answer was wrong in either arm, but the Grep arm was complete on 41 of 42 questions and the Onfly arm on 32. Every one of B's partials is an answer that says it did not read something, or is imprecise about a detail the code settles; the Onfly arm stops earlier. Its agents made twice the calls, because `onfly_focus` often returned the wrong symbols for a question that describes behavior (Cobra's completion, Hugo's shortcodes and cascade, Kubernetes' scheduler and job controller) and a second or third query was needed, then a native call for the detail it did not return. The Laravel result of the earlier runs (grep 4 of 8 against Onfly 8 of 8) did not repeat: the same Grep arm scored 8 of 8, in 5 calls, which suggests it answered from what the model already knows about Laravel. A framework that famous says little about an unfamiliar code base, and a comparison on one run of 8 questions says little about either.

**By kind of question** (my classification; one question can be argued into another group):

| Kind | Questions | A correct | B correct | Where Onfly did better or worse |
| --- | --- | --- | --- | --- |
| Call-graph, sequence and handler tracing ("what runs before and after", "who must change", "how does X reach Y") | 15 | 14 | 14 | Even: `called_by`, the run order in one `execute` and the flow through a handler were found in few calls. |
| Conceptual discovery ("where is X decided") | 20 | 20 | 15 | `onfly_focus` found the file on 15 of 20, but its second file was often missing (the sourcing of completion candidates, the help command, shortcode rendering and substitution), and B said what it had not seen. |
| Large-file anatomy (one long function or file whose parts decide the answer) | 5 | 5 | 1 | The skeleton gives the shape cheaply, but B did not expand the helper that held the detail (`rankDiskPressureFunc`, the concurrency policy, `setMetaPost`, the failure condition in the job controller). A read the file. |
| Config, env and literal lookup | 2 | 2 | 2 | Even. |

Where Onfly **saved time**: this repository (-38%), Hugo (-14%) and Kubernetes (-8%), the three where the Grep arm needed 9 to 20 calls; where it cost more time and tokens: Cobra and Laravel, where Grep answered in five calls. Where it **cost more tokens** than Grep: everywhere, by 23% overall, mostly the second and third `onfly_focus` queries and the bundles they return. Where it **gave a more complete answer**: once, on Laravel's retry question, where the Grep answer named a function the handler does not call; not on the whole.

`onfly_xray` costs 40 to 60% of the tokens of reading the files it covers (Cobra 41%, Hugo 57%, this repository 53%), and a file with no function bodies costs about the same as the file.

What the numbers support: on questions that describe behavior in words, in repositories of 40 to 4,000 files, the Grep arm was as complete and used less. They do not show that Onfly helps an agent that already searches well; they show where it is weak (the second file of a flow, the long function whose helper decides the answer, the hedged answer) and that its retrieval works on its own (the recall table above).
