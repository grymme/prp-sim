#!/bin/sh
# Linux install: load the Docker image and copy the GNS3 appliances.
#
# Steps:
#   1. chmod +x install.sh && ./install.sh        (loads the image)
#   2. In GNS3: File -> Import appliance
#        - gns3/westermo-prp.gns3a            (RedBox / DAN node)
#        - gns3/iec61850-publisher.gns3a      (GOOSE/SV publisher IED)
#        - gns3/iec61850-subscriber.gns3a     (GOOSE/SV subscriber IED)
#   3. Build your topology: publisher on SAN-A, RedBoxes in between,
#      subscriber on SAN-B. No internet needed — the image is local.
set -eu

echo "==> loading prp-sim image (may take a minute)..."
docker load -i prp-sim-image.tar.gz
echo "==> done. In GNS3: File -> Import appliance for each .gns3a in gns3/"
echo "    (a GNS3 restart may be needed before the new templates appear)"
