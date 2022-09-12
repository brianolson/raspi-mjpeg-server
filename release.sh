#!/bin/bash
set -e
set -x

today=$(date +%Y%m%d)
GOARCH=arm GOOS=linux go build -o raspi-mjpeg-server-arm-linux
gzip < raspi-mjpeg-server-arm-linux > raspi-mjpeg-server-arm-linux-${today}.gz
#xz < raspi-mjpeg-server-arm-linux > raspi-mjpeg-server-arm-linux-${today}.xz
# xz only saves about 0.5 MB out of 3MB. meh.

GOARCH=arm64 GOOS=linux go build -o raspi-mjpeg-server-arm64-linux
gzip < raspi-mjpeg-server-arm64-linux > raspi-mjpeg-server-arm64-linux-${today}.gz
#xz < raspi-mjpeg-server-arm64-linux > raspi-mjpeg-server-arm64-linux-${today}.xz
