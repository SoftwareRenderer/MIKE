#!/usr/bin/env bash
# Install gVisor (runsc) for use with podman, from the release bundle.
#
# Supports both system-wide (root) and user-local (rootless) installation:
#   sudo bash scripts/install-gvisor.sh   → /usr/local/bin (all users)
#   bash scripts/install-gvisor.sh        → ~/.local/bin   (current user)
#
# GVISOR_RELEASE pins a release, spelled as its bare date ("20260928"): the bucket path already
# carries the word `release`, so "release-20260928" is a 404. Same spelling as the image build arg
# of the same name, so one value pins both (see docs/docker.md).
#
# gVisor works with rootless podman using --runtime-flag ignore-cgroups=true.
# See: https://gvisor.dev/docs/user_guide/rootless/

set -euo pipefail

ARCH="$(uname -m)"
if [[ "$ARCH" != "x86_64" && "$ARCH" != "aarch64" ]]; then
    echo "ERROR: Unsupported architecture: $ARCH (need x86_64 or aarch64)" >&2
    exit 1
fi

# `tar --zstd` runs the zstd binary rather than a library, so a minimal host needs it installed.
if ! command -v zstd >/dev/null 2>&1; then
    echo "ERROR: zstd not found — install it first (apt-get install zstd, dnf install zstd)." >&2
    exit 1
fi

URL="https://storage.googleapis.com/gvisor/releases/release/${GVISOR_RELEASE:-latest}/${ARCH}"

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

# The published sha512 names the bundle by its bare name, so the check runs in the directory holding
# both files — and it runs before anything here is executed.
echo "Downloading gvisor.tar.zstd..."
wget -nv "${URL}/gvisor.tar.zstd" -O "${TMPDIR}/gvisor.tar.zstd"
wget -nv "${URL}/gvisor.tar.zstd.sha512" -O "${TMPDIR}/gvisor.tar.zstd.sha512"
echo "Verifying checksum..."
(cd "$TMPDIR" && sha512sum -c gvisor.tar.zstd.sha512)

# runsc executes the binaries under gvisor-bin/ at runtime and looks for that directory beside its
# own, so the tree is extracted whole and left whole: one binary out of the bundle is not an install.
# A single-binary install from before this layout is overwritten by this run's runsc.
echo "Extracting to ${INSTALL_DIR}..."
tar --zstd -xf "${TMPDIR}/gvisor.tar.zstd" -C "$INSTALL_DIR"

# runsc answers --version happily with gvisor-bin/ missing and fails only on a create, so the
# sidecar tree is what proves this install rather than runsc's own voice.
if [[ ! -x "${INSTALL_DIR}/gvisor-bin/gvisor_sentry" ]]; then
    echo "ERROR: ${INSTALL_DIR}/gvisor-bin/gvisor_sentry is missing after extraction." >&2
    exit 1
fi

# Verify installation
if "$INSTALL_DIR/runsc" --version >/dev/null 2>&1; then
    VERSION=$("$INSTALL_DIR/runsc" --version 2>&1 | head -1)
    echo ""
    echo "gVisor installed successfully!"
    echo "  Binary:   ${INSTALL_DIR}/runsc"
    echo "  Sidecars: ${INSTALL_DIR}/gvisor-bin/"
    echo "  Version:  ${VERSION}"
    echo ""
    echo "Verify with podman:"
    echo "  podman run --rm --runtime runsc --security-opt label=disable alpine echo hello"
else
    echo "ERROR: runsc installed but version check failed" >&2
    exit 1
fi
