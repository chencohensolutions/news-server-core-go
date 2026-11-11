#!/usr/bin/env sh
# Builds news-server here with the packs listed in packs.txt (see packs.example.txt); without packs.txt, the plain
# server. Extra arguments go to newsbuild: -o <file> to write the program elsewhere, --with <pack> instead of
# packs.txt. Run it again after pulling the core or a pack.
set -e
cd "$(dirname "$0")"
exec go run ./cmd/newsbuild "$@"
