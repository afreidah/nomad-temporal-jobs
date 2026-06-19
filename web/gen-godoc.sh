#!/bin/sh
# -----------------------------------------------------------------------------
# Generate gomarkdoc API-reference pages, one per Go package.
#
# Packages are discovered dynamically with `go list`, so adding or renaming one
# needs no edit here, in the Makefile, or in web/Dockerfile -- the package list
# lives in exactly zero places. Worker entrypoints (package main under */worker)
# are skipped, and the "internal/" path segment is dropped from the page name
# (maintenance/internal/nodes -> maintenance-nodes).
#
# Usage: web/gen-godoc.sh <output-dir>   (run from the repo root)
# -----------------------------------------------------------------------------
set -eu

out="${1:?usage: gen-godoc.sh <output-dir>}"
module=$(go list -m)
mkdir -p "$out"

go list ./... | grep -vE '/worker$' | while IFS= read -r pkg; do
	rel="${pkg#"$module"/}"
	name=$(printf '%s' "$rel" | sed 's#internal/##' | tr '/' '-')
	echo "  godoc: $rel"
	printf -- '---\ntitle: "%s"\n---\n\n' "$name" >"$out/$name.md"
	gomarkdoc "./$rel" >>"$out/$name.md"
	# Drop gomarkdoc's leading "# <pkg>" H1 so the Hugo front-matter title wins.
	sed -i '0,/^# /{/^# /d}' "$out/$name.md"
done
