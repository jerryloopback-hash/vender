#!/usr/bin/env bash
#
# Install vender for the current user, no root required.
#
#   scripts/install.sh                    # ~/.local/bin/vender + sing-box
#   scripts/install.sh --prefix /opt/vender
#   scripts/install.sh --autostart        # also start it at login
#   scripts/install.sh --uninstall
#
# The two binaries are installed side by side because that is one of the places
# vender looks for the engine: `vender` finds a `sing-box` (or `sing-box.exe`)
# in its own directory without any configuration. Everything is written under
# the prefix you choose — nothing outside it, and never as root.
#
# Options:
#   --prefix DIR     install prefix (default $HOME/.local); binaries go in DIR/bin
#   --bindir DIR     install the binaries directly into DIR
#   --autostart      run `vender autostart on` after installing
#   --no-sing-box    do not install the engine (use engine.binary or PATH)
#   --from DIR       take the binaries from DIR instead of building/fetching
#   --uninstall      remove the installed binaries and the autostart entry
#   -h, --help       this text
#
# Environment:
#   PREFIX           same as --prefix
#   SING_BOX_VERSION override the pinned sing-box version to fetch
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

PREFIX="${PREFIX:-$HOME/.local}"
BINDIR=""
AUTOSTART=0
UNINSTALL=0
INSTALL_SINGBOX=1
FROM=""

log() { printf '==> %s\n' "$*"; }
die() {
	printf 'install: %s\n' "$*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--prefix) PREFIX="${2:?--prefix needs a directory}"; shift 2 ;;
	--bindir) BINDIR="${2:?--bindir needs a directory}"; shift 2 ;;
	--autostart) AUTOSTART=1; shift ;;
	--no-autostart) AUTOSTART=0; shift ;;
	--no-sing-box) INSTALL_SINGBOX=0; shift ;;
	--from) FROM="${2:?--from needs a directory}"; shift 2 ;;
	--uninstall) UNINSTALL=1; shift ;;
	-h | --help) sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option \"$1\" (see --help)" ;;
	esac
done

[ -n "$BINDIR" ] || BINDIR="${PREFIX}/bin"

if [ "$UNINSTALL" = "1" ]; then
	# Remove the autostart entry first, so nothing tries to launch a binary
	# that is about to disappear. A missing entry is not an error.
	for exe in "${BINDIR}/vender" "${BINDIR}/vender.exe"; do
		if [ -x "$exe" ]; then
			"$exe" autostart off >/dev/null 2>&1 || true
		fi
	done
	rm -f "${BINDIR}/vender" "${BINDIR}/vender.exe" "${BINDIR}/sing-box" "${BINDIR}/sing-box.exe"
	log "removed vender from ${BINDIR}"
	exit 0
fi

# --- platform ----------------------------------------------------------------
uname_s="$(uname -s)"
case "$uname_s" in
Linux) os=linux ;;
Darwin) os=darwin ;;
MINGW* | MSYS* | CYGWIN*) os=windows ;;
*) die "unsupported platform: ${uname_s}" ;;
esac
arch="$(uname -m)"
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
esac

# --- the vender binary -------------------------------------------------------
if [ -n "$FROM" ]; then
	vender_src="${FROM}/vender"
	[ -f "$vender_src" ] || vender_src="${FROM}/vender.exe"
	[ -f "$vender_src" ] || die "${FROM} does not contain a vender binary"
elif [ -x "${ROOT}/dist/vender" ]; then
	vender_src="${ROOT}/dist/vender"
else
	log "building vender"
	(cd "$ROOT" && go build -o dist/vender ./cmd/vender) || die "go build failed"
	vender_src="${ROOT}/dist/vender"
fi

# --- the sing-box engine -----------------------------------------------------
singbox_src=""
if [ "$INSTALL_SINGBOX" = "1" ]; then
	for candidate in \
		"${ROOT}/dist/sing-box/${os}-${arch}/sing-box" \
		"${ROOT}/dist/sing-box/${os}-${arch}/sing-box.exe"; do
		[ -f "$candidate" ] && singbox_src="$candidate" && break
	done
	if [ -z "$singbox_src" ] && [ -x "${ROOT}/scripts/fetch-sing-box.sh" ]; then
		log "fetching the pinned sing-box"
		"${ROOT}/scripts/fetch-sing-box.sh" "${os}-${arch}" || true
		for candidate in \
			"${ROOT}/dist/sing-box/${os}-${arch}/sing-box" \
			"${ROOT}/dist/sing-box/${os}-${arch}/sing-box.exe"; do
			[ -f "$candidate" ] && singbox_src="$candidate" && break
		done
	fi
	if [ -z "$singbox_src" ]; then
		printf 'install: could not find a sing-box for %s-%s; install it yourself and\n' "$os" "$arch" >&2
		printf '         set engine.binary in config.yaml, or run scripts/fetch-sing-box.sh\n' >&2
	fi
fi

# --- install -----------------------------------------------------------------
mkdir -p "$BINDIR"
install -m 0755 "$vender_src" "${BINDIR}/vender"
log "installed ${BINDIR}/vender"
if [ -n "$singbox_src" ]; then
	install -m 0755 "$singbox_src" "${BINDIR}/$(basename "$singbox_src")"
	log "installed ${BINDIR}/$(basename "$singbox_src")"
fi

case ":${PATH}:" in
*":${BINDIR}:"*) ;;
*)
	printf '\n%s is not on your PATH. Add it to your shell profile, e.g.:\n' "$BINDIR"
	printf '  export PATH="%s:$PATH"\n' "$BINDIR"
	;;
esac

if [ "$AUTOSTART" = "1" ]; then
	log "enabling start at login"
	"${BINDIR}/vender" autostart on
fi

cat <<EOF

Next steps:
  vender import 'vless://<uuid>@<host>:443?...'   # add a server
  vender daemon                                   # run it in the foreground
  vender tui                                      # or use the terminal UI
EOF
