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

# Builds all NVSentinel images and pushes to ECR.
# Generates an ecr-values.yaml file for Helm to use the ECR images.
#
# Prerequisites:
#   - ECR repositories must already exist (run create-ecr-repos.sh first)
#   - AWS credentials configured
#   - ko installed (for Go modules)
#   - Docker with buildx support
#
# Usage:
#   ECR_REGISTRY="123456789012.dkr.ecr.us-east-1.amazonaws.com/nvsentinel-uat" ./build-push-images.sh
#
# Output:
#   - All images pushed to ECR
#   - /tmp/nvsentinel-ecr-values.yaml generated for Helm

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

AWS_REGION="${AWS_REGION:-us-east-1}"
ECR_REGISTRY="${ECR_REGISTRY:-}"
IMAGE_TAG="${IMAGE_TAG:-$(git -C "$REPO_ROOT" rev-parse --short HEAD)}"
ECR_VALUES_FILE="${ECR_VALUES_FILE:-/tmp/nvsentinel-ecr-values.yaml}"

# Validate required inputs
if [[ -z "$ECR_REGISTRY" ]]; then
    error "ECR_REGISTRY must be set. Run create-ecr-repos.sh first to get the registry URL."
fi

ecr_login() {
    log "Authenticating to ECR..."
    local ecr_base
    ecr_base=$(echo "$ECR_REGISTRY" | cut -d'/' -f1)
    
    if ! aws ecr get-login-password --region "$AWS_REGION" | \
        docker login --username AWS --password-stdin "$ecr_base"; then
        error "Failed to authenticate to ECR"
    fi
    
    log "ECR authentication successful ✓"
}

build_ko_images() {
    log "Building Go images with ko..."
    log "  Registry: $ECR_REGISTRY"
    log "  Tag: $IMAGE_TAG"
    
    cd "$REPO_ROOT"
    
    # ko uses KO_DOCKER_REPO for the registry
    export KO_DOCKER_REPO="$ECR_REGISTRY"
    
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
    log "  Registry: $ECR_REGISTRY"
    log "  Tag: $IMAGE_TAG"
    
    cd "$REPO_ROOT"
    
    # Extract registry base and org from ECR_REGISTRY
    # ECR_REGISTRY format: 123456789012.dkr.ecr.us-east-1.amazonaws.com/nvsentinel-uat
    local ecr_base
    local ecr_org
    ecr_base=$(echo "$ECR_REGISTRY" | cut -d'/' -f1)
    ecr_org=$(echo "$ECR_REGISTRY" | cut -d'/' -f2)
    
    # Common make variables
    local make_vars=(
        "CONTAINER_REGISTRY=$ecr_base"
        "CONTAINER_ORG=$ecr_org"
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

generate_ecr_values_file() {
    log "Generating ECR values file: $ECR_VALUES_FILE"
    
    cat > "$ECR_VALUES_FILE" << EOF
# Auto-generated ECR values file for NVSentinel UAT
# Generated at: $(date -u +"%Y-%m-%dT%H:%M:%SZ")
# ECR Registry: $ECR_REGISTRY
# Image Tag: $IMAGE_TAG

global:
  image:
    tag: "$IMAGE_TAG"

# Platform Connectors
platformConnector:
  image:
    repository: $ECR_REGISTRY/platform-connectors

# Fault Quarantine
fault-quarantine:
  image:
    repository: $ECR_REGISTRY/fault-quarantine

# Fault Remediation
fault-remediation:
  image:
    repository: $ECR_REGISTRY/fault-remediation
  logCollector:
    image:
      repository: $ECR_REGISTRY/nvsentinel/log-collector

# Node Drainer
node-drainer:
  image:
    repository: $ECR_REGISTRY/node-drainer

# Labeler
labeler:
  image:
    repository: $ECR_REGISTRY/labeler

# Janitor
janitor:
  image:
    repository: $ECR_REGISTRY/janitor

# Janitor Provider
janitor-provider:
  image:
    repository: $ECR_REGISTRY/janitor-provider

# Event Exporter
event-exporter:
  image:
    repository: $ECR_REGISTRY/event-exporter

# Kubernetes Object Monitor
kubernetes-object-monitor:
  image:
    repository: $ECR_REGISTRY/kubernetes-object-monitor

# GPU Health Monitor (Makefile adds nvsentinel/ prefix)
gpu-health-monitor:
  image:
    repository: $ECR_REGISTRY/nvsentinel/gpu-health-monitor
    tag: "$IMAGE_TAG-dcgm-4.x"

# Syslog Health Monitor (Makefile adds nvsentinel/ prefix)
syslog-health-monitor:
  image:
    repository: $ECR_REGISTRY/nvsentinel/syslog-health-monitor

# Metadata Collector (Makefile adds nvsentinel/ prefix)
metadata-collector:
  image:
    repository: $ECR_REGISTRY/nvsentinel/metadata-collector

# File Server Cleanup (Makefile adds nvsentinel/ prefix)
file-server-cleanup:
  image:
    repository: $ECR_REGISTRY/nvsentinel/file-server-cleanup
EOF
    
    log "ECR values file generated successfully ✓"
    log "Use with: helm install ... --values $ECR_VALUES_FILE"
}

main() {
    log "========================================="
    log "Building and Pushing NVSentinel Images"
    log "========================================="
    log "ECR Registry: $ECR_REGISTRY"
    log "Image Tag: $IMAGE_TAG"
    log "Values File: $ECR_VALUES_FILE"
    log "========================================="
    
    # Verify prerequisites
    if ! command -v ko &> /dev/null; then
        error "ko is required but not installed. Install from: https://ko.build/install/"
    fi
    
    if ! command -v docker &> /dev/null; then
        error "docker is required but not installed"
    fi
    
    ecr_login
    build_ko_images
    build_docker_images
    generate_ecr_values_file
    
    log "========================================="
    log "All images built and pushed successfully!"
    log "========================================="
    log "ECR Values File: $ECR_VALUES_FILE"
}

main "$@"

