# Onfly evaluation verdict

Two evaluations, same method: six repositories, one run per arm, Arm A = grep and read only, Arm B =
`onfly_focus` and `onfly_xray`, every answer graded by a separate agent that read the source.

- Run 1 (symbol-named questions, build `6b18b6d`): questions name the function or class.
- Run 2 (conceptual questions, build `c694f3a` plus the focus-body fix): questions describe behavior and name no symbol.

## Run 2: conceptual questions (current)

| Repo | Qs | A correct | B correct | A tokens | B tokens | A calls | B calls | A s | B s |
|---|---|---|---|---|---|---|---|---|---|
| Cobra (Go, small) | 8 | 7 | 8 | 50.7k | 60.1k | 6 | 7 | 50 | 39 |
| Hugo (Go) | 8 | 6 | 6 | 65.9k | 63.6k | 11 | 11 | 84 | 72 |
| Onfly repo (Go) | 10 | 10 | 6 | 59.2k | 58.0k | 16 | 10 | 59 | 80 |
| Laravel (PHP) | 8 | 5 | 5 | 46.4k | 60.3k | 4 | 6 | 30 | 68 |
| Kubernetes (Go, pkg/cmd) | 8 | 5 | 8 | 62.1k | 66.2k | 8 | 12 | 67 | 76 |
| PHP monorepo | 8 | 7 | 5 | 63.3k | 55.4k | 10 | 8 | 52 | 81 |
| **Total** | **50** | **40 (80%)** | **38 (76%)** | **347.6k** | **363.6k (+4.6%)** | **55** | **54** | 341 | 416 |

Seconds are indicative only: twelve agents ran in parallel on one machine. Tokens and calls are the harness's counts.

Compared with run 1: the token gap fell from +18% to +4.6%, and the accuracy gap from 10 points to 4 points
(grep 96% to 80%, Onfly 86% to 76%). Both arms scored lower because the questions are harder and
agents answered "from names" or said they had not read a body, which the graders marked PARTIAL.

### Where each arm won

- Onfly ahead: Kubernetes (8/8 against 5/8) and Cobra (8/8 against 7/8). Grep answers there stopped at function names or admitted unread branches; Onfly's answers covered eviction ordering, the cron missed-schedule path and the GC finalizer logic.
- Grep ahead: the Onfly repo (10/10 against 6/10) and the PHP monorepo (7/8 against 5/8). The Onfly-arm answers left out the callers or the second half of the flow (for the monorepo it led with the cashback end-of-cycle handler rather than the statement one, the same confusion as run 1) and said which code they had not read.
- Ties: Hugo (6/6) and Laravel (5/5).
- Tokens: Onfly was cheaper on 3 of 6 repos (Hugo, Onfly repo, monorepo) and dearer on the other three, by up to 30% on Laravel.

### What the agents reported broke

- `onfly_xray` with `File.php:method` or `file.go:Symbol` returned the file skeleton and not the function body on large files. Cobra (twice), Laravel, Kubernetes and Hugo agents all hit it. Cause: the skeleton is cut to the token budget before the expanded body, so the body fell off the end. Fixed after the run (the focused body is now a separate section; test added). The run 2 numbers are from before that fix.
- `onfly_focus` returned unrelated symbols for some symbol-less queries on a lexical index (monorepo, Kubernetes, Laravel).
- Retrieval check (`onfly eval --file-level`, recall@5 on the new symbol-less cases, lexical index): Hugo 0.29, Cobra 0.62, Onfly repo 0.30, Laravel 0.00, Kubernetes 0.12, monorepo 0.14. Embeddings changed nothing for Cobra and the Onfly repo (0.62 and 0.30). Hugo's embedding run had not finished, so its figure is lexical.

## Verdict

On conceptual questions Onfly roughly ties a grep-and-read agent: 76% against 80% correct, about 5% more tokens, the same number of tool calls. It does not yet save tokens or improve accuracy overall, but it is no longer clearly worse, and on Kubernetes-sized code it answered more completely. Retrieval of symbol-less queries is the weak point: the lexical index finds the right file in the top 5 for roughly one case in five on four of the six repositories.

The two fixes that this run supports: expand function bodies reliably (done) and improve ranking of paraphrased queries. Do not claim token savings until a repeat run, ideally three runs per arm, shows it.

## Run 1: symbol-named questions

Questions name the function or class (grep's best case).

| Repo | Qs | A correct | B correct | A tokens | B tokens | A calls | B calls (native) | A s | B s |
|---|---|---|---|---|---|---|---|---|---|
| Cobra (Go, small) | 8 | 8 | 8 | 48.8k | 57.0k | 5 | 6 (3) | 31 | 38 |
| Hugo (Go) | 8 | 8 | 7 | 54.8k | 69.9k | 8 | 13 (6) | 70 | 78 |
| Onfly repo (Go) | 10 | 10 | 10 | 66.5k | 77.8k | 12 | 15 (5) | 66 | 68 |
| Laravel (PHP) | 8 | 6 | 5 | 48.0k | 56.5k | 4 | 14 (2) | 31 | 57 |
| Kubernetes (Go, pkg/cmd) | 8 | 8 | 5 | 60.3k | 75.4k | 9 | 12 (1) | 83 | 53 |
| PHP monorepo | 8 | 8 | 8 | 59.4k | 60.7k | 8 | 9 (3) | 47 | 46 |
| **Total** | **50** | **48 (96%)** | **43 (86%)** | **337.8k** | **397.3k (+18%)** | **46** | **69 (+50%), 20 native** | 328 | 340 |

Run 1 findings: tokens were higher in all six repos, `called_by` was noisy or incomplete (Hugo missed
`commandeer.go:194`, Laravel missed non-test `tooManyAttempts` callers, Kubernetes mixed in every `NewManager`),
collapsed bodies were not expanded (Kubernetes Q8, Laravel Q6 and Q8), and `onfly_focus` was weak on the
PHP monorepo. Run 1's MX Arm A figures are from the earlier verified run.

## Limits of both runs

- One run per arm; a one-question difference (2%) is noise.
- Arm B was told to prefer Onfly, which inflates its calls and removes its freedom to skip it.
- In run 2 the Onfly-repo and Hugo indexes were being embedded in the background while Arm B ran, so those two arms searched a partly embedded index. Cobra had embeddings; the others were lexical.
- Graders are agents; they were asked to read the source and graded "I did not read this" as PARTIAL.
