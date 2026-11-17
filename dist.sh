#!/usr/bin/env bash
# 交叉编译 WallLoop 双架构发布包:dist/wallloop_<ver>_linux_{amd64,arm64}.tar.gz
# tarball 内含 bin/wallloop(封装仓 build.sh 按 bin/wallloop 成员名提取)。
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
VER="$(grep -oP '(?<=^const version = ")[^"]+' cmd/wallloop/main.go)"
OUT="dist"
mkdir -p "${OUT}"

for arch in amd64 arm64; do
    echo "== GOOS=linux GOARCH=${arch}"
    CGO_ENABLED=0 GOOS=linux GOARCH="${arch}" go build -trimpath -ldflags "-s -w" \
        -o "${OUT}/wallloop_linux_${arch}/bin/wallloop" ./cmd/wallloop
    tar -czf "${OUT}/wallloop_${VER}_linux_${arch}.tar.gz" -C "${OUT}/wallloop_linux_${arch}" bin
    sha256sum "${OUT}/wallloop_${VER}_linux_${arch}.tar.gz"
done

echo "== done:"
ls -lh "${OUT}"/wallloop_${VER}_linux_*.tar.gz
