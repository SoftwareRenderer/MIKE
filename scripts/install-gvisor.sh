#!/usr/bin/env bash
# Install gVisor (runsc) for use with podman.
# Downloads the latest release binaries from Google's storage bucket.
#
# Supports both system-wide (root) and user-local (rootless) installation:
#   sudo bash scripts/install-gvisor.sh   → /usr/local/bin (all users)
#   bash scripts/install-gvisor.sh        → ~/.local/bin   (current user)
#
# gVisor works with rootless podman using --runtime-flag ignore-cgroups=true.
# See: https://gvisor.dev/docs/user_guide/rootless/

set -euo pipefail

ARCH="$(uname -m)"
if [[ "$ARCH" != "x86_64" && "$ARCH" != "aarch64" ]]; then
    echo "ERROR: Unsupported architecture: $ARCH (need x86_64 or aarch64)" >&2
    exit 1
fi

URL="https://storage.googleapis.com/gvisor/releases/release/latest/${ARCH}"

# Choose install directory based on privileges
if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="${HOME}/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

echo "Installing gVisor (runsc) to ${INSTALL_DIR}..."
echo "Architecture: ${ARCH}"
echo "Source: ${URL}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

# Download runsc and checksum
echo "Downloading runsc..."
wget -q "${URL}/runsc" -O "${TMPDIR}/runsc"
wget -q "${URL}/runsc.sha512" -O "${TMPDIR}/runsc.sha512"

# Download containerd shim (optional, for containerd users)
echo "Downloading containerd-shim-runsc-v1..."
wget -q "${URL}/containerd-shim-runsc-v1" -O "${TMPDIR}/containerd-shim-runsc-v1" || true
wget -q "${URL}/containerd-shim-runsc-v1.sha512" -O "${TMPDIR}/containerd-shim-runsc-v1.sha512" || true

# Verify checksums
echo "Verifying checksums..."
(cd "$TMPDIR" && sha512sum -c runsc.sha512)

# Install
chmod a+rx "${TMPDIR}/runsc"
cp "${TMPDIR}/runsc" "${INSTALL_DIR}/runsc"

if [[ -f "${TMPDIR}/containerd-shim-runsc-v1" ]]; then
    chmod a+rx "${TMPDIR}/containerd-shim-runsc-v1"
    cp "${TMPDIR}/containerd-shim-runsc-v1" "${INSTALL_DIR}/containerd-shim-runsc-v1"
fi

# Verify installation
if "$INSTALL_DIR/runsc" --version >/dev/null 2>&1; then
    VERSION=$("$INSTALL_DIR/runsc" --version 2>&1 | head -1)
    echo ""
    echo "gVisor installed successfully!"
    echo "  Binary: ${INSTALL_DIR}/runsc"
    echo "  Version: ${VERSION}"
    echo ""
    echo "Verify with podman:"
    echo "  podman run --rm --runtime runsc --security-opt label=disable alpine echo hello"
else
    echo "ERROR: runsc installed but version check failed" >&2
    exit 1
fi
