#!/usr/bin/env bash
#
# cpa installer.
#
#   curl -fsSL https://raw.githubusercontent.com/shichao-wang/cpa/main/install.sh | bash
#
# Downloads the release archive for this platform, verifies its SHA-256
# against the published checksums.txt, and installs the binary.
#
# Environment variables:
#   CPA_VERSION       release tag to install (default: the latest release)
#   CPA_INSTALL_DIR   target directory (default: ~/.local/bin)
#   CPA_SKIP_VERIFY   set to 1 to skip checksum verification (not advised)
#
# Nothing here needs root: it installs into your home directory by default.

set -euo pipefail

REPO="shichao-wang/cpa"
BIN="cpa"

say()  { printf '%s\n' "$*"; }
die()  { printf 'install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

need curl
need tar
need uname

# ---- platform ----
case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux)  os=linux  ;;
  *)
    die "unsupported OS: $(uname -s).
On Windows, run this inside WSL, or install from source:
  go install github.com/${REPO}/cmd/${BIN}@latest"
    ;;
esac

case "$(uname -m)" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

# ---- version ----
tag="${CPA_VERSION:-}"
if [ -z "$tag" ]; then
  # Resolve the latest tag from the redirect rather than the API, which
  # would rate-limit unauthenticated installs.
  effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest")" \
    || die "could not reach github.com to resolve the latest release"
  case "$effective" in
    */releases/tag/*) tag="${effective##*/}" ;;
    *) die "no published release found for ${REPO}.
Install from source instead:
  go install github.com/${REPO}/cmd/${BIN}@latest" ;;
  esac
fi
[ -n "$tag" ] || die "could not determine which version to install"
ver="${tag#v}"

archive="${BIN}_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${tag}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "cpa ${tag} (${os}/${arch})"

# ---- download ----
say "  fetching ${archive}"
curl -fsSL -o "${tmp}/${archive}" "${base}/${archive}" \
  || die "download failed: ${base}/${archive}"

# ---- verify ----
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    die "no SHA-256 tool available (looked for sha256sum, shasum, openssl)"
  fi
}

if [ "${CPA_SKIP_VERIFY:-0}" = "1" ]; then
  say "  warning: checksum verification skipped (CPA_SKIP_VERIFY=1)"
else
  curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt" \
    || die "could not fetch checksums.txt; refusing to install an unverified binary"
  # Tolerate the "./" or "*" prefixes that different sha256 tools emit.
  expected="$(awk -v f="$archive" '
    { n = $2; sub(/^\.\//, "", n); sub(/^\*/, "", n); if (n == f) { print $1; exit } }
  ' "${tmp}/checksums.txt")"
  [ -n "$expected" ] || die "checksums.txt has no entry for ${archive}"
  actual="$(sha256_of "${tmp}/${archive}")"
  [ "$expected" = "$actual" ] \
    || die "checksum mismatch for ${archive}
  expected ${expected}
  actual   ${actual}
Refusing to install."
  say "  checksum ok"
fi

# ---- install ----
tar -xzf "${tmp}/${archive}" -C "$tmp" || die "could not extract ${archive}"
src="${tmp}/${BIN}_${ver}_${os}_${arch}/${BIN}"
[ -f "$src" ] || die "${archive} did not contain ${BIN}"

dir="${CPA_INSTALL_DIR:-${HOME}/.local/bin}"
mkdir -p "$dir" || die "cannot create ${dir}"
# Stage next to the target so the replacement is atomic.
stage="${dir}/.${BIN}.tmp.$$"
cp "$src" "$stage" || die "cannot write to ${dir} (set CPA_INSTALL_DIR to a writable directory)"
chmod 0755 "$stage"
mv -f "$stage" "${dir}/${BIN}" || die "cannot replace ${dir}/${BIN}"

say "  installed ${dir}/${BIN}"

case ":${PATH}:" in
  *":${dir}:"*) ;;
  *)
    say ""
    say "${dir} is not on your PATH. Add it, then restart your shell:"
    say "  export PATH=\"${dir}:\$PATH\""
    ;;
esac

say ""
say "next:"
say "  ${BIN} init                      # write ~/.config/cpa/settings.json"
say "  ${BIN} doctor                    # check your gateway"
say "  ${BIN} claude --profile <name>   # launch Claude Code"
