#!/usr/bin/env bash
#
# Build release archives in dist/release/.
#
#   dist/release/vender-<version>-linux-amd64.tar.gz
#   dist/release/vender-<version>-windows-amd64.zip
#
# Each archive holds a top-level vender-<version>-<target>/ directory with:
#
#   vender / vender.exe          the CLI, daemon and TUI
#   sing-box / sing-box.exe      the bundled proxy engine
#   README.md                    what it is, how to install and run it
#   LICENSE                      vender's licence: GNU GPL v3
#   NOTICE                       why sing-box is bundled, and its terms
#   LICENSE.sing-box             sing-box's own licence text
#
# Usage:
#   scripts/package.sh                 # linux-amd64 + windows-amd64
#   scripts/package.sh linux-amd64     # one target
#   scripts/package.sh --help
#
# Environment:
#   VERSION=x.y.z      version to stamp (default: git describe)
#   OUT=dir           output directory (default: dist/release)
#   SKIP_FETCH=1      fail instead of downloading sing-box when it is missing
#
# Requires: go, tar, and (zip or python3) for the Windows archive.
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

SING_BOX_VERSION="${SING_BOX_VERSION:-1.14.3}"
SINGBOX_ROOT="${SINGBOX_ROOT:-${ROOT}/dist/sing-box}"
OUT="${OUT:-${ROOT}/dist/release}"

ALL_TARGETS=(linux-amd64 windows-amd64)

log() { printf '==> %s\n' "$*"; }
die() {
	printf 'package: %s\n' "$*" >&2
	exit 1
}

usage() { sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'; }

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
esac

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

targets=("$@")
if [ "${#targets[@]}" -eq 0 ]; then
	targets=("${ALL_TARGETS[@]}")
fi
for t in "${targets[@]}"; do
	case "$t" in
	linux-amd64 | windows-amd64) ;;
	*) die "unsupported target \"$t\" (want linux-amd64 or windows-amd64)" ;;
	esac
done

# --- licence documents every archive must carry ------------------------------
for f in LICENSE NOTICE README.md; do
	[ -f "${ROOT}/${f}" ] || die "${f} is missing from the repository root"
done

# --- the binaries ------------------------------------------------------------
build_targets=()
for t in "${targets[@]}"; do build_targets+=("${t%%-*}"); done
log "building vender ${VERSION} (${build_targets[*]})"
"${ROOT}/scripts/build.sh" "${build_targets[@]}"

singbox_bin() {
	case "${1%%-*}" in
	windows) echo "${SINGBOX_ROOT}/${1}/sing-box.exe" ;;
	*) echo "${SINGBOX_ROOT}/${1}/sing-box" ;;
	esac
}
vender_bin() {
	case "${1%%-*}" in
	windows) echo "${ROOT}/dist/vender.exe" ;;
	*) echo "${ROOT}/dist/vender" ;;
	esac
}

# sing-box is fetched, not committed (it is ~80 MB and git-ignored), so a fresh
# clone needs it before an archive can be assembled.
for t in "${targets[@]}"; do
	bin="$(singbox_bin "$t")"
	[ -f "$bin" ] && continue
	if [ "${SKIP_FETCH:-0}" = "1" ]; then
		die "$bin is missing; run scripts/fetch-sing-box.sh $t first (SKIP_FETCH=1)"
	fi
	log "sing-box for $t is missing; fetching it"
	"${ROOT}/scripts/fetch-sing-box.sh" "$t"
	[ -f "$bin" ] || die "sing-box for $t is still missing after the fetch"
done

# --- archive helpers ---------------------------------------------------------
# Reproducible-ish: a fixed mtime, sorted entries and no owner metadata, so two
# runs over identical inputs produce identical archives. Bit-for-bit
# reproducible cross-builds are M7's; this at least removes the easy sources of
# churn.
make_tar() {
	local out="$1" dir="$2" top="$3"
	tar --create --gzip \
		--file "$out" \
		--directory "$dir" \
		--sort=name \
		--owner=0 --group=0 --numeric-owner \
		--mtime="@${SOURCE_DATE_EPOCH:-1000000000}" \
		"$top" 2>/dev/null ||
		tar -czf "$out" -C "$dir" "$top" # portable fallback
}

make_zip() {
	local out="$1" dir="$2" top="$3"
	if command -v zip >/dev/null 2>&1; then
		(cd "$dir" && zip -qr "$out" "$top")
		return
	fi
	command -v python3 >/dev/null 2>&1 || die "zip (or python3) is required for the Windows archive"
	python3 - "$out" "$dir" "$top" <<-'PY'
	import os, stat, sys, zipfile
	out, root, top = sys.argv[1], sys.argv[2], sys.argv[3]
	base = os.path.join(root, top)
	with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
	    for dirpath, dirnames, filenames in os.walk(base):
	        dirnames.sort()
	        for name in sorted(filenames):
	            full = os.path.join(dirpath, name)
	            rel = os.path.join(top, os.path.relpath(full, base))
	            info = zipfile.ZipInfo(rel, date_time=(2001, 9, 9, 1, 46, 40))
	            info.compress_type = zipfile.ZIP_DEFLATED
	            mode = 0o755 if name.endswith(".exe") else 0o644
	            info.external_attr = (stat.S_IFREG | mode) << 16
	            with open(full, "rb") as fh:
	                z.writestr(info, fh.read())
	PY
}

# --- assemble ----------------------------------------------------------------
mkdir -p "$OUT"
STAGE="${OUT}/.stage"
rm -rf "$STAGE"
mkdir -p "$STAGE"

for t in "${targets[@]}"; do
	os="${t%%-*}"
	top="vender-${VERSION}-${t}"
	dir="${STAGE}/${top}"
	mkdir -p "$dir"

	vbin="$(vender_bin "$t")"
	sbin="$(singbox_bin "$t")"
	[ -f "$vbin" ] || die "$vbin was not produced by scripts/build.sh"

	case "$os" in
	linux) install -m 0755 "$vbin" "$dir/vender" ;;
	windows)
		install -m 0755 "$vbin" "$dir/vender.exe"
		install -m 0755 "${ROOT}/dist/vender-daemon.exe" "$dir/vender-daemon.exe"
		mkdir -p "$dir/scripts/windows" "$dir/docs"
		install -m 0644 "${ROOT}/scripts/windows/setup.ps1" "$dir/scripts/windows/setup.ps1"
		install -m 0644 "${ROOT}/scripts/windows/setup-lib.ps1" "$dir/scripts/windows/setup-lib.ps1"
		install -m 0644 "${ROOT}/scripts/windows/test-setup.ps1" "$dir/scripts/windows/test-setup.ps1"
		install -m 0644 "${ROOT}/docs/WINDOWS-MANUAL-INSTALL.md" "$dir/docs/WINDOWS-MANUAL-INSTALL.md"
		;;
	esac
	install -m 0755 "$sbin" "$dir/$(basename "$sbin")"
	# Preserve optional libraries from the official engine archive, not a DLL site.
	for lib in libcronet.dll libcronet.so; do
		if [ -f "${SINGBOX_ROOT}/${t}/$lib" ]; then
			install -m 0755 "${SINGBOX_ROOT}/${t}/$lib" "$dir/$lib"
		fi
	done
	install -m 0644 "${ROOT}/README.md" "$dir/README.md"
	install -m 0644 "${ROOT}/LICENSE" "$dir/LICENSE"
	install -m 0644 "${ROOT}/NOTICE" "$dir/NOTICE"
	# sing-box's own licence text, shipped next to the binary it covers.
	install -m 0644 "${SINGBOX_ROOT}/${t}/LICENSE" "$dir/LICENSE.sing-box"

	archive="${OUT}/${top}.$( [ "$os" = windows ] && echo zip || echo tar.gz )"
	rm -f "$archive"
	case "$os" in
	linux) make_tar "$archive" "$STAGE" "$top" ;;
	windows) make_zip "$archive" "$STAGE" "$top" ;;
	esac
	log "wrote $(basename "$archive")"
done

rm -rf "$STAGE"

log "release archives in ${OUT} (vender ${VERSION}, commit ${COMMIT}, ${DATE})"
for t in "${targets[@]}"; do
	os="${t%%-*}"
	archive="${OUT}/vender-${VERSION}-${t}.$( [ "$os" = windows ] && echo zip || echo tar.gz )"
	printf '  %-8s %s\n' "$(du -h "$archive" | cut -f1)" "$(basename "$archive")"
done
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$OUT" && sha256sum vender-"${VERSION}"-*.tar.gz vender-"${VERSION}"-*.zip 2>/dev/null || true)
fi
