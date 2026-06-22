#!/usr/bin/env bash
# Populate ./corpus with a representative parity corpus:
#   - a Silesia-like mix (the real Silesia corpus if reachable)
#   - synthetic edge cases: zeros, random (incompressible), highly repetitive
#
# Large corpus files are NOT committed; this script regenerates them.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
corpus="$here/corpus"
mkdir -p "$corpus"

# --- Silesia (text/binary/db/already-structured mix) ---------------------------
if [ ! -f "$corpus/.silesia_done" ]; then
  url="http://sun.aei.polsl.pl/~sdeor/corpus/silesia.zip"
  tmp="$(mktemp -d)"
  if curl -fsSL --max-time 120 -o "$tmp/silesia.zip" "$url"; then
    unzip -o -q "$tmp/silesia.zip" -d "$corpus"
    touch "$corpus/.silesia_done"
  else
    echo "warning: could not download Silesia from $url; continuing with synthetic only" >&2
  fi
  rm -rf "$tmp"
fi

# --- Synthetic edge cases ------------------------------------------------------
python3 - "$corpus" <<'PY'
import os, sys, random
corpus = sys.argv[1]
random.seed(7)
N = 8 << 20
def w(name, data): open(os.path.join(corpus, name), "wb").write(data)
w("synth_zeros.bin",      b"\x00" * N)
w("synth_random.bin",     os.urandom(N))
w("synth_repetitive.bin", (b"The quick brown fox jumps over the lazy dog. " * 200000)[:N])
PY

echo "corpus ready in $corpus"
ls -la "$corpus" | grep -v '^total'
