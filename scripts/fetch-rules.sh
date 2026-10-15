#!/usr/bin/env bash
#
# Fetch vender's built-in routing rule sets (offline, "全部内置").
#
# Usage:
#   scripts/fetch-rules.sh              # download + verify into internal/rules/assets/
#   scripts/fetch-rules.sh --list       # show the pinned table (tag, source, sha256)
#   scripts/fetch-rules.sh --print-size # total embedded size in bytes/KiB
#   scripts/fetch-rules.sh --check      # verify the committed files against the table
#
# What this produces
#   internal/rules/assets/<tag>.srs     compiled sing-box rule sets (binary)
#
# The files are COMMITTED. vender embeds them with go:embed so the built binary
# needs no network and no data directory at runtime; a clean clone can run
# `go build ./...` with the assets already in place, and this script exists to
# re-fetch or to verify them (and to document their provenance).
#
# Why compiled .srs and not thousands of domains
#   The reference configuration this policy is derived from carries ~9500 rules.
#   Per-domain rules are enormous, rot quickly and cannot be reviewed. The
#   upstream project publishes the same data as category rule sets in sing-box's
#   binary format, which is exactly what route.rule_set consumes.
#
# Source and version
#   Repo    https://github.com/MetaCubeX/meta-rules-dat  (branch: sing)
#   Layout  geo/geosite/<name>.srs, geo/geoip/<name>.srs
#   Pinned  a commit SHA, never a branch or the moving `latest` tag, so a
#           rebuild fetches byte-identical inputs. Each file's SHA-256 is
#           pinned below and checked on every download and on --check.
#   MetaCubeX/meta-rules-dat is preferred over SagerNet/sing-geosite+sing-geoip
#   because it ships both families in one repo, one commit, one layout.
set -euo pipefail

# --- pinned source ----------------------------------------------------------
RULES_SOURCE_REPO="MetaCubeX/meta-rules-dat"
RULES_SOURCE_BRANCH="sing"
RULES_SOURCE_COMMIT="d1c433471f5b6b77051616cfa4a5279d5092f514"
RULES_SOURCE_DATE="2026-10-15" # committer date of the pinned commit

# tag|upstream path (relative to the branch root)|sha256
#
# tag is what internal/rules refers to and what shows up as the sing-box
# rule_set tag. Keep this table in sync with internal/rules/assets.go; a unit
# test fails if the two drift.
RULES_TABLE='
geosite-cn|geo/geosite/cn.srs|2ee13c9d9bc30b35554767fecc2f9511413b00e362b02fa6466800cce772fe20
geosite-category-ads-all|geo/geosite/category-ads-all.srs|5d7e3f38ceb008c793f12c4b84108ed61c64ae11e16a920af961e0c33a7e9233
geoip-cn|geo/geoip/cn.srs|4cdb3d4e6b82b5fb0603cfc078b80f775344a0f71f586dbce4d0ee47ac75a070
geosite-steam|geo/geosite/steam.srs|f9d7c11577f86eeaa315b188742cc40db1ef45994e29cb773c6268166b04c7bc
geosite-onedrive|geo/geosite/onedrive.srs|ecf26c6c2f999cc409fd2053128e361b48c1d7cdce130056f3cd2a6a74b0251c
geosite-microsoft|geo/geosite/microsoft.srs|3f84b0370ecb594755d84f9e1ed6cbf0eca1814431b7f8598f0b49c2ea52e25f
geosite-google|geo/geosite/google.srs|1f1a046775fbf136da45ab4435cee9450e0b98cab1f429e79d54604f073f8b15
geosite-youtube|geo/geosite/youtube.srs|ae9a576c046627c01bc1488f526775dfe919f1152c3ce6f75b678fce92498b3f
geosite-telegram|geo/geosite/telegram.srs|30cd392b1d79b0569af077874b36c6892bf13a94dce0d8e4410c3b1976d62ea2
geosite-netflix|geo/geosite/netflix.srs|40c5f50c80ceaba2a5e0b5c5760c9c5fce0b08498501397d8264dcae2aeeda53
geosite-openai|geo/geosite/openai.srs|d2c8fffc93d82084c4a01955891195cb7df84e1a804200de9e41868ac99e723b
geosite-bahamut|geo/geosite/bahamut.srs|b9ef2e31a6b359736aa618f2a717aa3530b66394a6c658dd0f16b77b67ef6174
'

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="${DEST:-${ROOT}/internal/rules/assets}"

die() {
	printf 'fetch-rules: %s\n' "$*" >&2
	exit 1
}

log() { printf '==> %s\n' "$*"; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; }

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "no sha256 tool available (need sha256sum or shasum)"
	fi
}

download_once() {
	local url="$1" out="$2"
	if command -v curl >/dev/null 2>&1; then
		# A TLS handshake to raw.githubusercontent.com gets cut mid-flight
		# often enough (proxies, TUNs, middleboxes) that a single attempt
		# fails intermittently with "unexpected eof" (curl error 35), which
		# --retry does not cover by itself.
		curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors -o "$out" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$out" "$url"
	else
		die "neither curl nor wget is available"
	fi
}

download() {
	local url="$1" out="$2" attempt
	for attempt in 1 2 3 4 5; do
		if download_once "$url" "$out"; then
			return 0
		fi
		rm -f "$out"
		if [ "$attempt" -lt 5 ]; then
			log "download attempt ${attempt} failed; retrying ${url}"
			sleep 2
		fi
	done
	return 1
}

raw_url() {
	printf 'https://raw.githubusercontent.com/%s/%s/%s\n' \
		"${RULES_SOURCE_REPO}" "${RULES_SOURCE_COMMIT}" "$1"
}

cmd_list() {
	printf '# %s @ %s (%s, branch %s)\n' \
		"${RULES_SOURCE_REPO}" "$(printf '%.12s' "${RULES_SOURCE_COMMIT}")" \
		"${RULES_SOURCE_DATE}" "${RULES_SOURCE_BRANCH}"
	printf '%-26s %-34s %s\n' TAG SOURCE SHA256
	while IFS='|' read -r tag path sha; do
		[ -n "$tag" ] || continue
		printf '%-26s %-34s %s\n' "$tag" "$path" "$sha"
	done <<<"${RULES_TABLE}"
}

cmd_print_size() {
	local total=0 n=0
	while IFS='|' read -r tag path sha; do
		[ -n "$tag" ] || continue
		local size
		size="$(stat -c%s "${DEST}/${tag}.srs" 2>/dev/null || echo 0)"
		total=$((total + size))
		n=$((n + 1))
	done <<<"${RULES_TABLE}"
	printf '%d rule sets, %d bytes (%.1f KiB) embedded\n' \
		"$n" "$total" "$(awk -v t="$total" 'BEGIN { printf "%.1f", t/1024 }')"
}

# verify_mode: check (files must exist and match) or fetch (download first)
cmd_check() {
	local failed=0
	while IFS='|' read -r tag path sha; do
		[ -n "$tag" ] || continue
		local file="${DEST}/${tag}.srs"
		if [ ! -f "$file" ]; then
			printf 'missing %s\n' "$file" >&2
			failed=1
			continue
		fi
		local got
		got="$(sha256_of "$file")"
		if [ "$got" != "$sha" ]; then
			printf 'checksum mismatch for %s\n  want %s\n  got  %s\n' "$file" "$sha" "$got" >&2
			failed=1
		fi
	done <<<"${RULES_TABLE}"
	if [ "$failed" -ne 0 ]; then
		die "the committed rule sets do not match the pinned table (re-run without --check)"
	fi
	log "ok: every committed rule set matches the pinned table"
}

cmd_fetch() {
	mkdir -p "$DEST"
	# A killed download must not leave a partial file behind for go:embed (or
	# the next --check) to trip over.
	cleanup() { rm -f "${DEST}"/*.srs.tmp.* 2>/dev/null || true; }
	trap cleanup EXIT
	local total=0
	while IFS='|' read -r tag path sha; do
		[ -n "$tag" ] || continue
		local file="${DEST}/${tag}.srs" tmp
		tmp="${file}.tmp.$$"
		log "fetching ${tag} <- ${path}"
		cleanup
		if ! download "$(raw_url "$path")" "$tmp"; then
			rm -f "$tmp"
			die "download failed: $(raw_url "$path")"
		fi
		local got
		got="$(sha256_of "$tmp")"
		if [ "$got" != "$sha" ]; then
			rm -f "$tmp"
			die "checksum mismatch for ${path}
  want ${sha}
  got  ${got}
refusing to install into ${DEST}"
		fi
		mv "$tmp" "$file"
		chmod 0644 "$file"
		total=$((total + $(stat -c%s "$file")))
	done <<<"${RULES_TABLE}"
	log "installed into ${DEST} (${total} bytes)"
	cmd_print_size
}

main() {
	case "${1:-}" in
	-h | --help | help)
		usage
		return 0
		;;
	--list | -l)
		cmd_list
		return 0
		;;
	--print-size)
		cmd_print_size
		return 0
		;;
	--check)
		cmd_check
		return 0
		;;
	"") cmd_fetch ;;
	*) die "unknown argument \"$1\" (see --help)" ;;
	esac
}

main "$@"
