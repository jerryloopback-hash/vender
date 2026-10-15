#!/usr/bin/env bash
#
# Fetch the pinned sing-box release for the targets vender ships.
#
# Usage:
#   scripts/fetch-sing-box.sh                 # linux/amd64 + windows/amd64
#   scripts/fetch-sing-box.sh linux-amd64     # a single target
#   scripts/fetch-sing-box.sh --print-config  # just show what vender should use
#
# Output layout (git-ignored; see .gitignore):
#   dist/sing-box/linux-amd64/{sing-box,LICENSE,libcronet.so}
#   dist/sing-box/windows-amd64/{sing-box.exe,LICENSE,libcronet.dll}
#
# The version is pinned below so a build is reproducible and a compromised
# upstream release cannot silently replace what vender runs. The pinned version
# is also what README.md and docs/ARCHITECTURE.md quote.
#
# To install it for vender:
#   * either copy the unpacked files next to the vender executable
#     (engine.binary stays empty and the "bundled" lookup finds it), or
#   * set engine.binary in config.yaml, e.g.
#         engine:
#             binary: /absolute/path/to/dist/sing-box/linux-amd64/sing-box
#
# scripts/install.sh and scripts/package.sh call this script when the pinned
# sing-box is missing, so a release archive or an install needs no manual step.
set -euo pipefail

# --- pinned release ---------------------------------------------------------
SING_BOX_VERSION="${SING_BOX_VERSION:-1.14.3}"
SING_BOX_TAG="v${SING_BOX_VERSION}"

# SHA-256 of the release archives for the pinned version, taken from the
# official GitHub release assets. Only checked when SING_BOX_VERSION is the
# pinned one (an override is the user's own call).
declare -A PINNED_SHA256=(
	["linux-amd64"]="e2bdf179c15a3652955dc44867e33ca0965c9c9b91b34267dcc5a5d639a5feee"
	["windows-amd64"]="d8b86ef58936dba9230ec6a57ffa4243933f3c14ef4809828b6c553440ed1518"
)

REPO="SagerNet/sing-box"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_ROOT="${DEST_ROOT:-${ROOT}/dist/sing-box}"

ALL_TARGETS=(linux-amd64 windows-amd64)

die() {
	# Never leave a partially downloaded archive behind.
	rm -f "${DEST_ROOT}"/.sing-box-* 2>/dev/null || true
	printf 'fetch-sing-box: %s\n' "$*" >&2
	exit 1
}

log() { printf '==> %s\n' "$*"; }

usage() {
	sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'
}

# asset_name <target> -> the release asset file name for that target
asset_name() {
	local target="$1" os="${1%%-*}" arch="${1##*-}"
	case "$os" in
	linux) echo "sing-box-${SING_BOX_VERSION}-${os}-${arch}.tar.gz" ;;
	windows) echo "sing-box-${SING_BOX_VERSION}-${os}-${arch}.zip" ;;
	*) die "unknown target \"$target\" (want linux-amd64 or windows-amd64)" ;;
	esac
}

download() {
	local url="$1" out="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --retry-delay 2 -o "$out" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$out" "$url"
	else
		die "neither curl nor wget is available"
	fi
}

verify_sha256() {
	local target="$1" archive="$2"
	local want="${PINNED_SHA256[$target]:-}"

	if [ -z "$want" ]; then
		log "no pinned checksum for $target; skipping verification"
		return 0
	fi
	if [ "${SING_BOX_VERSION}" != "1.14.3" ]; then
		log "SING_BOX_VERSION=${SING_BOX_VERSION} is not the pinned release; skipping checksum verification"
		return 0
	fi

	local got
	if command -v sha256sum >/dev/null 2>&1; then
		got="$(sha256sum "$archive" | cut -d' ' -f1)"
	elif command -v shasum >/dev/null 2>&1; then
		got="$(shasum -a 256 "$archive" | cut -d' ' -f1)"
	else
		log "no sha256 tool available; skipping verification"
		return 0
	fi

	if [ "$got" != "$want" ]; then
		die "checksum mismatch for $target
  want $want
  got  $got
refusing to install $archive"
	fi
	log "checksum ok for $target"
}

unpack() {
	local target="$1" os="${1%%-*}" archive="$2" dest="$3"

	rm -rf "$dest"
	mkdir -p "$dest"

	case "$os" in
	linux)
		command -v tar >/dev/null 2>&1 || die "tar is required to unpack $archive"
		# --strip-components drops the sing-box-<version>-<os>-<arch>/ prefix so
		# the binary lands directly in dist/sing-box/<os>-<arch>/.
		tar -xzf "$archive" -C "$dest" --strip-components=1
		;;
	windows)
		if command -v unzip >/dev/null 2>&1; then
			unzip -q -j "$archive" -d "$dest"
		elif command -v python3 >/dev/null 2>&1; then
			python3 - "$archive" "$dest" <<-'PY'
				import sys, zipfile
				zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])
				# flatten: archives contain a single top-level directory
				import os, shutil
				root = sys.argv[2]
				sub = [d for d in os.listdir(root) if os.path.isdir(os.path.join(root, d))]
				if len(sub) == 1:
				    inner = os.path.join(root, sub[0])
				    for name in os.listdir(inner):
				        os.replace(os.path.join(inner, name), os.path.join(root, name))
				    os.rmdir(inner)
			PY
		else
			die "unzip (or python3) is required to unpack $archive"
		fi
		;;
	esac

	# Keep the executables executable regardless of how the archive stored them.
	case "$os" in
	linux) chmod +x "$dest/sing-box" ;;
	windows) chmod +x "$dest/sing-box.exe" 2>/dev/null || true ;;
	esac
}

fetch_target() {
	local target="$1" os="${1%%-*}" arch="${1##*-}"
	local asset dest archive
	asset="$(asset_name "$target")"
	dest="${DEST_ROOT}/${target}"
	archive="${DEST_ROOT}/.${asset}"

	local url="https://github.com/${REPO}/releases/download/${SING_BOX_TAG}/${asset}"
	log "downloading ${asset}"
	# The archive is temporary: remove it on every exit path from here on.
	rm -f "$archive"
	download "$url" "$archive"
	verify_sha256 "$target" "$archive"
	unpack "$target" "$archive" "$dest"
	rm -f "$archive"

	local bin="${dest}/sing-box"
	[ "$os" = "windows" ] && bin="${dest}/sing-box.exe"
	[ -f "$bin" ] || die "$asset did not contain ${bin##*/}"

	log "installed $target -> $bin"
	if [ "$os" = "$(uname -s | tr '[:upper:]' '[:lower:]')" ] && [ "${arch}" = "$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" ]; then
		"$bin" version || die "$bin does not run"
	fi
}

main() {
	local targets=()

	for arg in "$@"; do
		case "$arg" in
		-h | --help)
			usage
			return 0
			;;
		--print-config)
			printf 'engine:\n    binary: %s\n' "${DEST_ROOT}/linux-amd64/sing-box"
			return 0
			;;
		-*) die "unknown option $arg (see --help)" ;;
		*) targets+=("$arg") ;;
		esac
	done
	if [ "${#targets[@]}" -eq 0 ]; then
		targets=("${ALL_TARGETS[@]}")
	fi
	for t in "${targets[@]}"; do
		case "$t" in
		linux-amd64 | windows-amd64) ;;
		*) die "unsupported target \"$t\" (want linux-amd64 or windows-amd64)" ;;
		esac
	done

	log "sing-box ${SING_BOX_VERSION} -> ${DEST_ROOT}"
	mkdir -p "$DEST_ROOT"
	for t in "${targets[@]}"; do
		fetch_target "$t"
	done
	log "done; point engine.binary at a binary above, or copy it next to vender"
}

main "$@"
