# Retrieval benchmark

Question sets and a script to score `onfly_focus` against a simple keyword search on real projects, so a change to ranking, chunking or parsing can be judged by numbers. A change that loses questions on any set should not be kept; see [docs/evaluation.md](../../docs/evaluation.md#large-repositories-and-other-languages) for how the results were read.

```sh
scripts/bench/fetch.sh repos                       # pinned shallow clones (Kubernetes: only pkg, cmd and apiserver)
cd repos/hugo && onfly init                        # lexical index, seconds; `onfly index` adds the vectors
python3 ../../scripts/bench/bench.py ../../scripts/bench/cases/hugo.yaml --min 12
```

`repos.txt` pins each project to a commit. `cases/` has one file per project (`onfly.yaml` is for this repository itself, run from its root). The questions for `laravel` and `kubernetes` are paraphrases that repeat none of the code's names, so they measure search by meaning; run them after `onfly index` with an embedder up for the hybrid number, and without one for the lexical number.

The script needs `onfly` on `PATH` and Python 3. It prints a hit or miss per question, the totals for `onfly_focus` and for the keyword baseline, token counts (bytes divided by four) and the median latency, and exits 1 when `--min` is not reached.
