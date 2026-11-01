#!/bin/sh
# Rebuilds ../qjs.wasm from pinned, checksummed sources (see ../VENDOR.md).
#
# Needs zig 0.15.x (it bundles clang, wasm-ld and wasi-libc), curl, tar, patch and shasum.
# Usage: build.sh [workdir] [out.wasm]
#   workdir defaults to <repo>/tmp/qjs-wasm-build, out.wasm to ../qjs.wasm.
#
# Differences from upstream's build (fastschema/qjs Makefile, wasi-sdk):
#   - quickjs-wasi-stack.patch turns QuickJS' stack limit on for WASI, so deep recursion
#     throws RangeError instead of running off the shadow stack.
#   - The shadow stack is STACK_SIZE bytes (upstream: wasm-ld's 64 KiB default) and sits at the
#     bottom of linear memory (zig passes --stack-first), so overflowing it traps cleanly
#     instead of overwriting static data.
#   - WASI reactor (exports _initialize, no _start/main).
#   - wasi-libc's dlmalloc is linked first so it replaces zig's default emmalloc (same
#     allocator as upstream's wasi-sdk build; emmalloc was ~20% slower and used ~25% more
#     memory on allocation-heavy scripts).
#   - No wasm-opt pass (not needed: CPU-bound benchmarks match upstream's binary).
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${1:-$(git -C "$HERE" rev-parse --show-toplevel)/tmp/qjs-wasm-build}
OUT=${2:-$HERE/../qjs.wasm}

# Shadow stack size and the part of it kept free below the JS stack limit for C code that runs
# between two checks. JS recursion depth scales with STACK_SIZE - STACK_RESERVE (1 MiB: ~3300
# plain calls). The wazero native stack grows ~30 MiB of transient Go memory per MiB at full
# depth and traps at ~50 MB, so keep STACK_SIZE at 1 MiB unless that is re-measured.
STACK_SIZE=${STACK_SIZE:-1048576}
STACK_RESERVE=${STACK_RESERVE:-131072}

# Pinned sources.
QUICKJS_COMMIT=d01ca4491fb24ccfeccb4c7394e28a3b21fd5986 # quickjs-ng v0.10.1, the fastschema/qjs v0.0.6 submodule
QUICKJS_SHA256=40be727abd6f7d24911b6a33fc3d25603a3f89eee183f74ffe6ca8d5619d9922
QJS_TAG=v0.0.6 # fastschema/qjs: the C glue in qjswasm/ and its export list
QJS_SHA256=0621b8a3f82b9f5f37a7b33f145f25d4248d92688da19738782db4ccb933eb12
WASI_LIBC_TAG=wasi-sdk-21 # dlmalloc as in wasi-sdk 21 (clang 17, what upstream's binary was built with)
DLMALLOC_WRAPPER_SHA256=f1f2d23b9c4a5fc611573e4c23b62ce8f7deb76e800d3d846d865d443722e00f
DLMALLOC_SHA256=270c0d65e7fe0f3574d761bcc3204cfc12a242dd2d41b684709587b6fb017cd2

command -v zig >/dev/null || { echo "build.sh: zig not found (brew install zig)" >&2; exit 1; }

# fetch URL FILE SHA256: downloads FILE once and verifies it.
fetch() {
	if [ ! -f "$2" ]; then
		curl -fsSL -o "$2.part" "$1"
		mv "$2.part" "$2"
	fi
	echo "$3  $2" | shasum -a 256 -c - >/dev/null || { echo "build.sh: checksum mismatch for $2" >&2; exit 1; }
}

mkdir -p "$WORK/dl"
fetch "https://codeload.github.com/quickjs-ng/quickjs/tar.gz/$QUICKJS_COMMIT" "$WORK/dl/quickjs.tar.gz" "$QUICKJS_SHA256"
fetch "https://codeload.github.com/fastschema/qjs/tar.gz/refs/tags/$QJS_TAG" "$WORK/dl/qjs.tar.gz" "$QJS_SHA256"
fetch "https://raw.githubusercontent.com/WebAssembly/wasi-libc/$WASI_LIBC_TAG/dlmalloc/src/dlmalloc.c" "$WORK/dl/dlmalloc.c" "$DLMALLOC_WRAPPER_SHA256"
fetch "https://raw.githubusercontent.com/WebAssembly/wasi-libc/$WASI_LIBC_TAG/dlmalloc/src/malloc.c" "$WORK/dl/malloc.c" "$DLMALLOC_SHA256"

# Fresh, patched source tree on every run.
SRC="$WORK/src"
rm -rf "$SRC"
mkdir -p "$SRC/qjs" "$SRC/dlmalloc"
tar -xzf "$WORK/dl/qjs.tar.gz" -C "$SRC/qjs" --strip-components=1
rm -rf "$SRC/qjs/qjswasm/quickjs"
mkdir -p "$SRC/qjs/qjswasm/quickjs"
tar -xzf "$WORK/dl/quickjs.tar.gz" -C "$SRC/qjs/qjswasm/quickjs" --strip-components=1
(cd "$SRC/qjs/qjswasm/quickjs" && patch -s -p1 <"$HERE/quickjs-wasi-stack.patch")
cp "$WORK/dl/dlmalloc.c" "$WORK/dl/malloc.c" "$SRC/dlmalloc/"

GLUE="$SRC/qjs/qjswasm"
Q="$GLUE/quickjs"

# Same exports as upstream: every uncommented LINKER:--export= in qjswasm.cmake.
EXPORTS=$(grep -v '^[[:space:]]*#' "$GLUE/qjswasm.cmake" | grep -o 'LINKER:--export=[A-Za-z0-9_]*' |
	sed 's/^LINKER:/-Wl,/' | sort -u)

# dlmalloc.c must be the first object: wasm-ld extracts archive members lazily, so the first
# reference to malloc would otherwise pull zig's emmalloc out of libc.a.
# shellcheck disable=SC2086
zig cc -target wasm32-wasi -mexec-model=reactor \
	-O3 -s -std=gnu11 -funsigned-char -fvisibility=default -Wno-everything \
	-DNDEBUG -D_GNU_SOURCE -D_WASI_EMULATED_PROCESS_CLOCKS -D_WASI_EMULATED_SIGNAL \
	-DJS_DEFAULT_STACK_SIZE=0 -DJS_WASI_STACK_RESERVE="$STACK_RESERVE" \
	-I"$Q" \
	"$SRC/dlmalloc/dlmalloc.c" \
	"$Q/cutils.c" "$Q/dtoa.c" "$Q/libregexp.c" "$Q/libunicode.c" "$Q/quickjs.c" "$Q/quickjs-libc.c" \
	"$GLUE/eval.c" "$GLUE/function.c" "$GLUE/helpers.c" "$GLUE/proxy.c" "$GLUE/qjs.c" \
	-lwasi-emulated-process-clocks -lwasi-emulated-signal \
	-Wl,-z,stack-size="$STACK_SIZE" \
	$EXPORTS \
	-o "$OUT"

echo "built $OUT ($(wc -c <"$OUT" | tr -d ' ') bytes, stack $STACK_SIZE, reserve $STACK_RESERVE)"
