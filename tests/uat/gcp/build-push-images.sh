#!/bin/bash
# Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Builds all NVSentinel images and pushes to Google Artifact Registry.
# Generates a gar-values.yaml file for Helm to use the GAR images.
#
# Prerequisites:
#   - GAR repository must already exist (run create-gar-repos.sh first)
#   - gcloud credentials configured
#   - ko installed (for Go modules)
#   - Docker with buildx support
#
# Usage:
#   GAR_REGISTRY="us-central1-docker.pkg.dev/my-project/nvsentinel-uat" ./build-push-images.sh
#
# Output:
#   - All images pushed to GAR
#   - /tmp/nvsentinel-gar-values.yaml generated for Helm

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

GCP_REGION="${GCP_REGION:-us-central1}"
GAR_REGISTRY="${GAR_REGISTRY:-}"
IMAGE_TAG="${IMAGE_TAG:-$(git -C "$REPO_ROOT" rev-parse --short HEAD)}"
GAR_VALUES_FILE="${GAR_VALUES_FILE:-/tmp/nvsentinel-gar-values.yaml}"

# Validate required inputs
if [[ -z "$GAR_REGISTRY" ]]; then
    error "GAR_REGISTRY must be set. Run create-gar-repos.sh first to get the registry URL."
fi

gar_login() {
    log "Authenticating to Google Artifact Registry..."
    
    # Extract region from GAR_REGISTRY (format: REGION-docker.pkg.dev/PROJECT/REPO)
    local gar_host
    gar_host=$(echo "$GAR_REGISTRY" | cut -d'/' -f1)
    
    # Configure Docker to use gcloud credentials for GAR
    if ! gcloud auth configure-docker "$gar_host" --quiet; then
        error "Failed to configure Docker for GAR authentication"
    fi
    
    log "GAR authentication configured ✓"
}

build_ko_images() {
    log "Building Go images with ko..."
    log "  Registry: $GAR_REGISTRY"
    log "  Tag: $IMAGE_TAG"
    
    cd "$REPO_ROOT"
    
    # ko uses KO_DOCKER_REPO for the registry
    export KO_DOCKER_REPO="$GAR_REGISTRY"
    
    # Set required environment variables for .ko.yaml templates
    export VERSION="$IMAGE_TAG"
    export GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')}"
    export BUILD_DATE="$(date -u +%FT%TZ)"
    
    # Build all ko images (amd64 only for speed)
    if ! ko build -B --sbom=none --tags="$IMAGE_TAG" --platform=linux/amd64 \
        ./event-exporter \
        ./fault-quarantine \
        ./fault-remediation \
        ./health-monitors/kubernetes-object-monitor \
        ./janitor \
        ./janitor-provider \
        ./labeler \
        ./node-drainer \
        ./platform-connectors; then
        error "Failed to build ko images"
    fi
    
    log "Ko images built and pushed successfully ✓"
}

build_docker_images() {
    log "Building Docker images..."
    log "  Registry: $GAR_REGISTRY"
    log "  Tag: $IMAGE_TAG"
    
    cd "$REPO_ROOT"
    
    # Extract registry host and path from GAR_REGISTRY
    # GAR_REGISTRY format: us-central1-docker.pkg.dev/project-id/nvsentinel-uat
    local gar_host
    local gar_path
    gar_host=$(echo "$GAR_REGISTRY" | cut -d'/' -f1)
    gar_path=$(echo "$GAR_REGISTRY" | cut -d'/' -f2-)
    
    # Common make variables
    local make_vars=(
        "CONTAINER_REGISTRY=$gar_host"
        "CONTAINER_ORG=$gar_path"
        "CI_COMMIT_REF_NAME=$IMAGE_TAG"
        "PLATFORMS=linux/amd64"
        "DISABLE_REGISTRY_CACHE=true"
    )
    
    # GPU Health Monitor (DCGM 4.x)
    log "  Building gpu-health-monitor..."
    if ! make -C health-monitors/gpu-health-monitor docker-publish-dcgm4 "${make_vars[@]}"; then
        error "Failed to build gpu-health-monitor"
    fi
    
    # Syslog Health Monitor
    log "  Building syslog-health-monitor..."
    if ! make -C health-monitors/syslog-health-monitor docker-publish "${make_vars[@]}"; then
        error "Failed to build syslog-health-monitor"
    fi
    
    # Log Collector
    log "  Building log-collector..."
    if ! make -C log-collector docker-publish "${make_vars[@]}"; then
        error "Failed to build log-collector"
    fi
    
    # Metadata Collector
    log "  Building metadata-collector..."
    if ! make -C metadata-collector docker-publish "${make_vars[@]}"; then
        error "Failed to build metadata-collector"
    fi
    
    log "Docker images built and pushed successfully ✓"
}

generate_gar_values_file() {
    log "Generating GAR values file: $GAR_VALUES_FILE"
    
    cat > "$GAR_VALUES_FILE" << EOF
# Auto-generated GAR values file for NVSentinel UAT
# Generated at: $(date -u +"%Y-%m-%dT%H:%M:%SZ")
# GAR Registry: $GAR_REGISTRY
# Image Tag: $IMAGE_TAG

global:
  image:
    tag: "$IMAGE_TAG"

# Platform Connectors
platformConnector:
  image:
    repository: $GAR_REGISTRY/platform-connectors

# Fault Quarantine
fault-quarantine:
  image:
    repository: $GAR_REGISTRY/fault-quarantine

# Fault Remediation
fault-remediation:
  image:
    repository: $GAR_REGISTRY/fault-remediation
  logCollector:
    image:
      repository: $GAR_REGISTRY/nvsentinel/log-collector

# Node Drainer
node-drainer:
  image:
    repository: $GAR_REGISTRY/node-drainer

# Labeler
labeler:
  image:
    repository: $GAR_REGISTRY/labeler

# Janitor
janitor:
  image:
    repository: $GAR_REGISTRY/janitor

# Janitor Provider
janitor-provider:
  image:
    repository: $GAR_REGISTRY/janitor-provider

# Event Exporter
event-exporter:
  image:
    repository: $GAR_REGISTRY/event-exporter

# Kubernetes Object Monitor
kubernetes-object-monitor:
  image:
    repository: $GAR_REGISTRY/kubernetes-object-monitor

# GPU Health Monitor (Makefile adds nvsentinel/ prefix)
gpu-health-monitor:
  image:
    repository: $GAR_REGISTRY/nvsentinel/gpu-health-monitor
    tag: "$IMAGE_TAG-dcgm-4.x"

# Syslog Health Monitor (Makefile adds nvsentinel/ prefix)
syslog-health-monitor:
  image:
    repository: $GAR_REGISTRY/nvsentinel/syslog-health-monitor

# Metadata Collector (Makefile adds nvsentinel/ prefix)
metadata-collector:
  image:
    repository: $GAR_REGISTRY/nvsentinel/metadata-collector
EOF
    
    log "GAR values file generated successfully ✓"
    log "Use with: helm install ... --values $GAR_VALUES_FILE"
}

main() {
    log "========================================="
    log "Building and Pushing NVSentinel Images"
    log "========================================="
    log "GAR Registry: $GAR_REGISTRY"
    log "Image Tag: $IMAGE_TAG"
    log "Values File: $GAR_VALUES_FILE"
    log "========================================="
    
    # Verify prerequisites
    if ! command -v ko &> /dev/null; then
        error "ko is required but not installed. Install from: https://ko.build/install/"
    fi
    
    if ! command -v docker &> /dev/null; then
        error "docker is required but not installed"
    fi
    
    if ! command -v gcloud &> /dev/null; then
        error "gcloud is required but not installed"
    fi
    
    gar_login
    build_ko_images
    build_docker_images
    generate_gar_values_file
    
    log "========================================="
    log "All images built and pushed successfully!"
    log "========================================="
    log "GAR Values File: $GAR_VALUES_FILE"
}

main "$@"

