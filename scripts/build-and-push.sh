#!/bin/bash
set -e

# Build and Push Script for TrueNAS CSI Driver
# This script helps build and push the Docker image to various registries

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Version information - derived from git if available
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo "dev")}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")}"
BUILD_DATE="${BUILD_DATE:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}"

# Default values
IMAGE_TAG="${IMAGE_TAG:-${VERSION}}"
REGISTRY_TYPE="${REGISTRY_TYPE:-dockerhub}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -t, --type TYPE       Registry type: dockerhub, ghcr, local (default: dockerhub)"
    echo "  -u, --user USER       Registry username (required for dockerhub/ghcr)"
    echo "  -i, --tag TAG         Image tag (default: git-derived version)"
    echo "  -p, --push            Push image after building (default: false)"
    echo "  -h, --help            Show this help message"
    echo ""
    echo "Environment variables:"
    echo "  VERSION               Override version (default: git describe)"
    echo "  GIT_COMMIT            Override commit SHA (default: git rev-parse)"
    echo "  BUILD_DATE            Override build date (default: current UTC time)"
    echo ""
    echo "Examples:"
    echo "  # Build for Docker Hub"
    echo "  $0 --type dockerhub --user myusername --tag v0.5.0 --push"
    echo ""
    echo "  # Build for GitHub Container Registry"
    echo "  $0 --type ghcr --user myusername --tag v0.5.0 --push"
    echo ""
    echo "  # Build for local testing (kind/minikube)"
    echo "  $0 --type local --tag latest"
    echo ""
    exit 1
}

error() {
    echo -e "${RED}ERROR: $1${NC}" >&2
    exit 1
}

info() {
    echo -e "${GREEN}INFO: $1${NC}"
}

warn() {
    echo -e "${YELLOW}WARN: $1${NC}"
}

# Parse arguments
PUSH=false
REGISTRY_USER=""

while [[ $# -gt 0 ]]; do
    case $1 in
        -t|--type)
            REGISTRY_TYPE="$2"
            shift 2
            ;;
        -u|--user)
            REGISTRY_USER="$2"
            shift 2
            ;;
        -i|--tag)
            IMAGE_TAG="$2"
            shift 2
            ;;
        -p|--push)
            PUSH=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            error "Unknown option: $1"
            ;;
    esac
done

# Validate registry type
case $REGISTRY_TYPE in
    dockerhub|ghcr|local)
        ;;
    *)
        error "Invalid registry type: $REGISTRY_TYPE. Must be dockerhub, ghcr, or local"
        ;;
esac

# Validate username for remote registries
if [[ "$REGISTRY_TYPE" != "local" ]] && [[ -z "$REGISTRY_USER" ]]; then
    error "Registry username required for $REGISTRY_TYPE. Use --user option."
fi

# Set image name based on registry type
case $REGISTRY_TYPE in
    dockerhub)
        IMAGE_NAME="${REGISTRY_USER}/tns-csi-driver"
        ;;
    ghcr)
        IMAGE_NAME="ghcr.io/${REGISTRY_USER}/tns-csi-driver"
        ;;
    local)
        IMAGE_NAME="tns-csi-driver"
        ;;
esac

FULL_IMAGE="${IMAGE_NAME}:${IMAGE_TAG}"
LATEST_IMAGE="${IMAGE_NAME}:latest"

info "Building TrueNAS CSI Driver Docker image"
info "Registry Type: $REGISTRY_TYPE"
info "Image: $FULL_IMAGE"

# Change to project root
cd "$PROJECT_ROOT"

# Build the image
info "Building Docker image..."
info "Version: $VERSION"
info "Git Commit: $GIT_COMMIT"
info "Build Date: $BUILD_DATE"
docker build \
    --build-arg VERSION="$VERSION" \
    --build-arg GIT_COMMIT="$GIT_COMMIT" \
    --build-arg BUILD_DATE="$BUILD_DATE" \
    -t "$FULL_IMAGE" -t "$LATEST_IMAGE" .

if [[ $? -ne 0 ]]; then
    error "Docker build failed"
fi

info "Successfully built: $FULL_IMAGE"
info "Successfully built: $LATEST_IMAGE"

# Handle local registry loading
if [[ "$REGISTRY_TYPE" == "local" ]]; then
    info "Checking for local Kubernetes clusters..."
    
    # Check for kind
    if command -v kind &> /dev/null; then
        KIND_CLUSTERS=$(kind get clusters 2>/dev/null)
        if [[ -n "$KIND_CLUSTERS" ]]; then
            info "Found kind clusters:"
            echo "$KIND_CLUSTERS"
            read -p "Load image into kind cluster? (y/n) " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                read -p "Enter cluster name (or press enter for 'kind'): " CLUSTER_NAME
                CLUSTER_NAME=${CLUSTER_NAME:-kind}
                info "Loading image into kind cluster: $CLUSTER_NAME"
                kind load docker-image "$FULL_IMAGE" --name "$CLUSTER_NAME"
                info "Image loaded into kind cluster"
            fi
        fi
    fi
    
    # Check for minikube
    if command -v minikube &> /dev/null; then
        if minikube status &> /dev/null; then
            info "Found running minikube cluster"
            read -p "Load image into minikube? (y/n) " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Yy]$ ]]; then
                info "Loading image into minikube..."
                minikube image load "$FULL_IMAGE"
                info "Image loaded into minikube"
            fi
        fi
    fi
    
    info "Local build complete!"
    exit 0
fi

# Push to remote registry if requested
if [[ "$PUSH" == true ]]; then
    info "Pushing images to $REGISTRY_TYPE..."
    
    # Check if already logged in
    case $REGISTRY_TYPE in
        dockerhub)
            info "Logging in to Docker Hub..."
            docker login
            ;;
        ghcr)
            info "Logging in to GitHub Container Registry..."
            if [[ -z "$GITHUB_TOKEN" ]]; then
                warn "GITHUB_TOKEN environment variable not set"
                info "You can create a token at: https://github.com/settings/tokens"
                info "Token needs 'write:packages' permission"
                read -sp "Enter GitHub Personal Access Token: " GITHUB_TOKEN
                echo
            fi
            echo "$GITHUB_TOKEN" | docker login ghcr.io -u "$REGISTRY_USER" --password-stdin
            ;;
    esac
    
    # Push images
    info "Pushing $FULL_IMAGE..."
    docker push "$FULL_IMAGE"
    
    info "Pushing $LATEST_IMAGE..."
    docker push "$LATEST_IMAGE"
    
    info "Successfully pushed images!"
    info ""
    info "To use this image with Helm:"
    info "  helm install tns-csi oci://registry-1.docker.io/${REGISTRY_USER}/tns-csi-driver --version ${IMAGE_TAG#v}"
else
    info ""
    info "Image built but not pushed. To push, run:"
    info "  $0 --type $REGISTRY_TYPE --user $REGISTRY_USER --tag $IMAGE_TAG --push"
fi

info ""
info "Next steps:"
info "1. Deploy with Helm:"
info "   helm install tns-csi ./charts/tns-csi-driver \\"
info "     --set truenas.url=\"wss://YOUR-TRUENAS-IP:443/api/current\" \\"
info "     --set truenas.apiKey=\"YOUR-API-KEY\" \\"
info "     --set storageClasses.nfs.server=\"YOUR-TRUENAS-IP\" \\"
info "     --set image.tag=\"$IMAGE_TAG\""
