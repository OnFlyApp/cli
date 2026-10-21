#!/bin/sh
# recall.sh <name> <checkout> : file-level recall@5 of a checkout that is already indexed,
# with the suites in bench/evals/<name>/. Columns: plain and onfly_focus ranking, lexical and hybrid.
set -e
here=$(cd "$(dirname "$0")" && pwd)
name=$1 dir=$2
for suite in eval eval.symbols; do
  cp "$here/evals/$name/$suite.yaml" "$dir/.onfly/$suite.yaml"
  for mode in "--lexical" "--lexical --focus" "" "--focus"; do
    printf '%-14s %-20s ' "$suite" "${mode:-hybrid}"
    (cd "$dir" && onfly eval --file-level --k 5 --cases ".onfly/$suite.yaml" $mode | grep -o 'recall@5: [0-9.]*')
  done
done
if [ -f "$here/evals/$name/eval.identifiers.yaml" ]; then
  cp "$here/evals/$name/eval.identifiers.yaml" "$dir/.onfly/eval.identifiers.yaml"
  printf '%-14s %-20s ' eval.identifiers focus
  (cd "$dir" && onfly eval --k 5 --cases .onfly/eval.identifiers.yaml --focus | grep -o 'recall@5: [0-9.]*')
fi
