#!/bin/sh
set -eu
missing=""
for tool in go make docker kind kubectl helm curl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    missing="$missing $tool"
  fi
done
if [ -n "$missing" ]; then
  echo "missing required tools:$missing" >&2
  exit 1
fi
echo "all required tools are installed"