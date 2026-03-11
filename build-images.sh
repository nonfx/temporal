#!/bin/sh
set -eu

# =============================================================================
# Build Temporal Docker images (server + admin-tools) for ECR
#
# Usage:
#   ./build-images.sh                    # build multi-arch, no push
#   ./build-images.sh --push             # build + push to ECR
#   ./build-images.sh --platform linux/amd64  # single arch (faster for testing)
#
# Environment overrides:
#   CLI_VERSION      - Temporal CLI version (default: 1.6.1)
#   CLI_REPO         - Path to patched CLI repo (default: ../temporal-cli)
#   IMAGE_REPO       - Docker image repository (default: temporaliotest)
#   IMAGE_TAG        - ECR image tag (default: v1.30.1-test)
#   ALPINE_TAG       - Alpine base image tag (default: 3.23.3)
# =============================================================================

# --- Check prerequisites ---
for cmd in goreleaser go docker git; do
    command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: $cmd not found"; exit 1; }
done

# --- Defaults (override via env) ---
CLI_VERSION="${CLI_VERSION:-1.6.1}"
CLI_REPO="${CLI_REPO:-../temporal-cli}"
IMAGE_REPO="${IMAGE_REPO:-temporaliotest}"
IMAGE_TAG="${IMAGE_TAG:-v1.30.1-test}"
ALPINE_TAG="${ALPINE_TAG:-3.23.3}"
PLATFORM="${PLATFORM:-}"
PUSH="${PUSH:-false}"

# --- Parse args ---
for arg in "$@"; do
    case "$arg" in
        --push) PUSH="true" ;;
        --platform=*) PLATFORM="${arg#--platform=}" ;;
        --platform) shift_next=true ;;
        *)
            if [ "${shift_next:-}" = "true" ]; then
                PLATFORM="$arg"
                shift_next=""
            fi
            ;;
    esac
done

# --- Validate platform ---
if [ -n "$PLATFORM" ]; then
    case "$PLATFORM" in
        linux/*) ;; # valid
        *) echo "ERROR: Invalid platform: $PLATFORM (expected linux/<arch>)"; exit 1 ;;
    esac
fi

# --- Determine architectures ---
if [ -n "$PLATFORM" ]; then
    ARCH=$(echo "$PLATFORM" | cut -d'/' -f2)
    ARCHS="$ARCH"
    echo "==> Single architecture build: $ARCH"
else
    ARCHS="amd64 arm64"
    echo "==> Multi-architecture build: amd64 arm64"
fi

echo "==> CLI version: $CLI_VERSION"
echo "==> Image repo: $IMAGE_REPO"
echo "==> Image tag: $IMAGE_TAG"
echo "==> Alpine tag: $ALPINE_TAG"
echo ""

# --- Clean stale build artifacts ---
rm -rf docker/build

# --- Step 1: Build binaries via GoReleaser ---
echo "==> Step 1/5: Building binaries with GoReleaser..."
export GOTOOLCHAIN=go1.25.8
goreleaser build --snapshot --clean

# --- Step 2: Organize binaries for Docker ---
echo ""
echo "==> Step 2/5: Organizing binaries for Docker..."

BINARIES="temporal-server temporal-cassandra-tool temporal-sql-tool temporal-elasticsearch-tool tdbg"

for arch in $ARCHS; do
    mkdir -p "docker/build/${arch}"

    # Map arch to GoReleaser dist suffix
    case "$arch" in
        amd64) dist_arch="amd64_v1" ;;
        arm64) dist_arch="arm64_v8.0" ;;
        *) echo "ERROR: Unsupported arch: $arch"; exit 1 ;;
    esac

    for binary in $BINARIES; do
        src="dist/${binary}_linux_${dist_arch}/${binary}"
        dst="docker/build/${arch}/${binary}"

        if [ ! -f "$src" ]; then
            echo "ERROR: Binary not found: $src"
            exit 1
        fi

        cp "$src" "$dst"
        chmod 755 "$dst"
        echo "  Copied $src -> $dst"
    done
done

# --- Step 3: Build Temporal CLI from source (patched fork) ---
echo ""
echo "==> Step 3/5: Building Temporal CLI v${CLI_VERSION} from source..."

if [ ! -d "$CLI_REPO" ]; then
    echo "ERROR: CLI repo not found at $CLI_REPO"
    echo "  Clone it with: git clone git@github.com:nonfx/cli.git ${CLI_REPO}"
    exit 1
fi

CLI_REPO_ABS=$(cd "$CLI_REPO" && pwd)
echo "  CLI repo: $CLI_REPO_ABS"

# CLI fork uses go1.26.1 (not 1.25.8) because CVE-2026-27137 fix
# is only available in go1.26.1+, not backported to the 1.25.x line.
for arch in $ARCHS; do
    echo "  Building CLI for linux/${arch}..."
    GOTOOLCHAIN=go1.26.1 GOOS=linux GOARCH=${arch} CGO_ENABLED=0 \
        go build -C "$CLI_REPO_ABS" -o "$(pwd)/docker/build/${arch}/temporal" ./cmd/temporal

    chmod 755 "docker/build/${arch}/temporal"
    echo "  Built CLI -> docker/build/${arch}/temporal"
done

# --- Step 4: Copy schema files ---
echo ""
echo "==> Step 4/5: Copying schema files..."

mkdir -p docker/build/temporal/schema
cp -r schema/cassandra schema/mysql schema/postgresql docker/build/temporal/schema/
echo "  Copied schema directories (cassandra, mysql, postgresql)"

# --- Step 5: Build Docker images ---
echo ""
echo "==> Step 5/5: Building Docker images..."

# Validate binaries are in place
echo "  Validating binaries..."
for arch in $ARCHS; do
    for binary in $BINARIES temporal; do
        if [ ! -f "docker/build/${arch}/${binary}" ]; then
            echo "ERROR: Missing docker/build/${arch}/${binary}"
            exit 1
        fi
    done
done
echo "  All binaries present."

# Extract server version from git tag (can't run linux binary on macOS)
SERVER_VERSION=$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo "unknown")
if [ "$SERVER_VERSION" = "unknown" ]; then
    echo "  WARNING: Could not determine server version from git tags"
fi
echo "  Server version: $SERVER_VERSION"

# Build with docker buildx bake
export IMAGE_REPO
export IMAGE_SHA_TAG="$IMAGE_TAG"
export IMAGE_BRANCH_TAG="custom-build"
export TEMPORAL_SHA=$(git rev-parse HEAD)
export TAG_LATEST="false"
export ALPINE_TAG
export SERVER_VERSION
export CLI_VERSION

# Use a named docker-container builder for multi-arch support
BUILDER="temporal-builder"
docker buildx inspect "$BUILDER" >/dev/null 2>&1 || \
    docker buildx create --name "$BUILDER" --driver docker-container

echo "  Using builder: $BUILDER"

if [ "$PUSH" = "true" ]; then
    echo "  Building and pushing..."
    if [ -n "$PLATFORM" ]; then
        docker buildx bake \
            --builder "$BUILDER" \
            --set "*.platform=${PLATFORM}" \
            --push \
            -f docker/docker-bake.hcl \
            server admin-tools
    else
        docker buildx bake \
            --builder "$BUILDER" \
            --push \
            -f docker/docker-bake.hcl \
            server admin-tools
    fi
else
    echo "  Building (no push)..."
    if [ -n "$PLATFORM" ]; then
        docker buildx bake \
            --builder "$BUILDER" \
            --set "*.platform=${PLATFORM}" \
            --load \
            -f docker/docker-bake.hcl \
            server admin-tools
    else
        # Multi-arch can't --load into local daemon, just build to validate
        docker buildx bake \
            --builder "$BUILDER" \
            -f docker/docker-bake.hcl \
            server admin-tools
    fi
fi

echo ""
echo "==> Build complete!"
echo "  Images tagged as: ${IMAGE_REPO}/server:${IMAGE_TAG} and ${IMAGE_REPO}/admin-tools:${IMAGE_TAG}"
echo ""
echo "  To scan:"
echo "    trivy image --severity CRITICAL,HIGH ${IMAGE_REPO}/server:${IMAGE_TAG}"
echo "    trivy image --severity CRITICAL,HIGH ${IMAGE_REPO}/admin-tools:${IMAGE_TAG}"
