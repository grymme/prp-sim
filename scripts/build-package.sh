#!/bin/sh
# build-package.sh — assemble the distributable ZIP for sharing PRP sim +
# IEC 61850 IED nodes with a colleague.
#
# The ZIP is self-contained: Docker image tarball (no GHCR pull needed),
# GNS3 appliances + symbols, the Windows traffic generator + Npcap
# installer, an install script and a README.
#
# Usage:  ./scripts/build-package.sh [version]
#         # version defaults to the current git tag or "dev"
set -eu

cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
PKG="prp-sim-${VERSION}"
STAGE="$(mktemp -d)/${PKG}"
mkdir -p "${STAGE}"

echo "==> building image prp-sim:${VERSION}"
docker build --build-arg VERSION="${VERSION}" -t "prp-sim:${VERSION}" -f Dockerfile . >/dev/null

echo "==> exporting image tarball (self-contained, no GHCR pull needed)"
docker save "prp-sim:${VERSION}" | gzip > "${STAGE}/prp-sim-image.tar.gz"

echo "==> copying GNS3 appliances + symbols"
mkdir -p "${STAGE}/gns3/symbols"
cp gns3/westermo-prp.gns3a \
   gns3/westermo-prp-edge.gns3a \
   gns3/iec61850-publisher.gns3a \
   gns3/iec61850-subscriber.gns3a "${STAGE}/gns3/"
cp gns3/symbols/prp-node.svg \
   gns3/symbols/iec61850-publisher.svg \
   gns3/symbols/iec61850-subscriber.svg "${STAGE}/gns3/symbols/"

echo "==> cross-compiling Windows traffic generator (GOOS=windows amd64)"
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X prp-gns3/internal/version.Version=${VERSION}" \
    -o "${STAGE}/trafficgen-windows-amd64.exe" ./cmd/trafficgen

echo "==> downloading Npcap installer (best effort; run as admin on Windows)"
NPDIR="${STAGE}/windows"
mkdir -p "${NPDIR}"
if command -v curl >/dev/null 2>&1; then
    if curl -fsSL -m 120 -o "${NPDIR}/npcap-installer.exe" \
        "https://npcap.com/dist/npcap-1.80.exe" 2>/dev/null; then
        echo "    npcap-installer.exe downloaded"
    else
        echo "    download failed — add manually from https://npcap.com (Npcap 1.80+)"
    fi
else
    echo "    curl not available — add npcap-installer.exe manually"
fi

echo "==> writing install script + README"
cp scripts/install.sh "${STAGE}/install.sh" 2>/dev/null || cat > "${STAGE}/install.sh" <<'EOF'
#!/bin/sh
# Linux install: load the Docker image and copy the GNS3 appliances.
# Import the .gns3a files in GNS3 (File -> Import appliance) and build
# your topology with the "PRP Node", "IEC 61850 IED Publisher" and
# "IEC 61850 IED Subscriber" templates.
set -eu
echo "==> loading prp-sim image (may take a minute)..."
docker load -i prp-sim-image.tar.gz
echo "==> done. In GNS3: File -> Import appliance for each .gns3a in gns3/"
EOF
chmod +x "${STAGE}/install.sh"

cp docs/README-package.md "${STAGE}/README.md"

echo "==> zipping"
OUT="dist/${PKG}.zip"
mkdir -p dist
( cd "$(dirname "${STAGE}")" && zip -qr "$(cd "${OLDPWD:-.}" >/dev/null && pwd)/${OUT}" "${PKG}" )
echo ""
echo "Package ready: ${OUT}"
du -h "${OUT}"
rm -rf "$(dirname "${STAGE}")"
