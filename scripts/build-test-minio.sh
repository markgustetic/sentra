#!/bin/sh
# MinIO's community distribution is source-only. Build the exact release used
# by the integration suite instead of relying on the removed registry image.
set -eu

context=$(mktemp -d)
trap 'rm -rf "$context"' EXIT HUP INT TERM

# Cross-compilation cannot use go install with GOBIN. Download/build in an
# isolated module so MinIO's dependencies never enter Sentra's go.mod/go.sum.
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in
    x86_64) arch=amd64 ;;
    aarch64) arch=arm64 ;;
esac
(
    cd "$context"
    go mod init sentra-minio-build
    go mod download -json github.com/minio/minio@07c3a429bfed433e49018cb0f78a52145d4bedeb > module.json
    source=$(go env GOMODCACHE)/github.com/minio/minio@$(go list -m -f '{{.Version}}' github.com/minio/minio@07c3a429bfed433e49018cb0f78a52145d4bedeb)
    cd "$source"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$context/minio" .
)

cat > "$context/Dockerfile" <<'EOF'
FROM scratch
COPY minio /minio
ENTRYPOINT ["/minio"]
EOF
docker build -t minio/minio:RELEASE.2025-09-07T16-13-09Z "$context"
