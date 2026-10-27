#!/bin/sh
# PowerDeck helper installer for Linux (systemd): servers, Raspberry Pi, NAS, desktops.
#
#   curl -fsSL https://github.com/ProjectZero02/powerdeck/releases/latest/download/install.sh | sh
#
# Run it as root, or as a user with sudo (it asks for your password and re-runs itself as root).
# Uninstall:          ... | sh -s -- --uninstall      (add --purge to delete keys and settings)
# Specific version:   ... | POWERDECK_VERSION=2.0.0 sh
#
# It downloads the helper for this CPU, checks it against the release's SHA256SUMS.txt and its
# Ed25519 signature, installs it as a systemd service and shows the QR code that adds this device
# to the PowerDeck app. Re-running it upgrades in place and keeps your settings.
set -eu

# Everything runs from main, called on the last line, so a download cut short never runs half a script.
main() {

REPO="ProjectZero02/powerdeck"
# Ed25519 key that signs every release (the same one the helper checks its own updates against).
PUBKEY_PEM="-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEACJxP8WfqitXsZh9tobWk/cyQbfMMVkk2+iLCHWYkucs=
-----END PUBLIC KEY-----"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf '\033[31mPowerDeck: %s\033[0m\n' "$*" >&2; exit 1; }
fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 3 -o "$2" "$1"
  else wget -q -O "$2" "$1"; fi
}

UNINSTALL=0; PURGE=""
for a in "$@"; do
  case "$a" in
    --uninstall) UNINSTALL=1 ;;
    --purge) PURGE="--purge" ;;
    *) die "unknown option $a" ;;
  esac
done

if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "run this as root (for example after su -)"
  say "PowerDeck installs a system service, so it needs root. sudo may ask for your password."
  case "$0" in
    *.sh) exec sudo env POWERDECK_VERSION="${POWERDECK_VERSION:-}" sh "$0" "$@" ;; # run from a saved file
  esac
  # Piped from curl: this copy of the script was read from stdin, so download it again and run that.
  SELF="https://github.com/$REPO/releases/latest/download/install.sh"
  [ -z "${POWERDECK_VERSION:-}" ] || SELF="https://github.com/$REPO/releases/download/v${POWERDECK_VERSION#v}/install.sh"
  ME=$(mktemp "${TMPDIR:-/var/tmp}/powerdeck-install.XXXXXX")
  fetch "$SELF" "$ME" && [ "$(head -c 9 "$ME")" = "#!/bin/sh" ] || { rm -f "$ME"; die "download failed: $SELF"; }
  exec sudo env POWERDECK_VERSION="${POWERDECK_VERSION:-}" sh -c 'sh "$0" "$@"; s=$?; rm -f "$0"; exit $s' "$ME" "$@"
fi

if [ "$UNINSTALL" -eq 1 ]; then
  [ -x /usr/local/bin/powerdeck-helper ] || die "PowerDeck isn't installed"
  /usr/local/bin/powerdeck-helper uninstall $PURGE
  say "PowerDeck removed."
  exit 0
fi

command -v systemctl >/dev/null 2>&1 || die "this system doesn't use systemd, which the installer needs"
command -v tailscale >/dev/null 2>&1 || die "Tailscale isn't installed. Install it first: curl -fsSL https://tailscale.com/install.sh | sh"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  armv7l|armv7*|armhf) ARCH=arm ;;
  riscv64) ARCH=riscv64 ;;
  *) die "unsupported CPU $(uname -m)" ;;
esac

if [ -n "${POWERDECK_VERSION:-}" ]; then
  BASE="https://github.com/$REPO/releases/download/v${POWERDECK_VERSION#v}"
else
  BASE="https://github.com/$REPO/releases/latest/download"
fi
ASSET="powerdeck-helper-linux-$ARCH.gz"

TMP=$(mktemp -d "${TMPDIR:-/var/tmp}/powerdeck.XXXXXX") # not /tmp, which may be noexec
trap 'rm -rf "$TMP"' EXIT INT TERM

say "Downloading PowerDeck for linux/$ARCH…"
fetch "$BASE/$ASSET" "$TMP/$ASSET" || die "download failed: $BASE/$ASSET"
fetch "$BASE/$ASSET.sig" "$TMP/$ASSET.sig" || die "download failed: $ASSET.sig"
fetch "$BASE/SHA256SUMS.txt" "$TMP/SHA256SUMS.txt" || die "download failed: SHA256SUMS.txt"

want=$(grep " $ASSET\$" "$TMP/SHA256SUMS.txt" | cut -d' ' -f1)
[ -n "$want" ] || die "$ASSET isn't listed in SHA256SUMS.txt"
if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1)
else got=$(shasum -a 256 "$TMP/$ASSET" | cut -d' ' -f1); fi
[ "$got" = "$want" ] || die "checksum mismatch for $ASSET (got $got, want $want)"

if command -v openssl >/dev/null 2>&1 && openssl version | grep -qE '^OpenSSL (1\.1\.1|[3-9])'; then
  printf '%s\n' "$PUBKEY_PEM" > "$TMP/pub.pem"
  base64 -d "$TMP/$ASSET.sig" > "$TMP/sig.bin" 2>/dev/null || openssl base64 -d -A -in "$TMP/$ASSET.sig" -out "$TMP/sig.bin"
  openssl pkeyutl -verify -pubin -inkey "$TMP/pub.pem" -rawin -in "$TMP/$ASSET" -sigfile "$TMP/sig.bin" >/dev/null 2>&1 \
    || die "signature check failed for $ASSET: refusing to install"
  say "Checksum and signature OK."
else
  say "Checksum OK (no OpenSSL 1.1.1+ here to check the signature too)."
fi

gzip -dc "$TMP/$ASSET" > "$TMP/powerdeck-helper"
chmod 0755 "$TMP/powerdeck-helper"
say "Installing…"
"$TMP/powerdeck-helper" install
}

main "$@"
