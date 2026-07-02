#!/usr/bin/env sh
set -eu

files="$(git ls-files '*.go')"
if [ -z "$files" ]; then
  exit 0
fi

unformatted="$(gofmt -l $files)"
if [ -n "$unformatted" ]; then
  printf '%s\n' 'gofmt needed for:'
  printf '%s\n' "$unformatted"
  exit 1
fi
