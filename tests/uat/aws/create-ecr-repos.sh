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

# Creates ECR repositories for all NVSentinel images.
# Outputs the ECR registry URL to stdout for use by other scripts.
#
# Usage:
#   ./create-ecr-repos.sh
#   ECR_REGISTRY=$(./create-ecr-repos.sh)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

# Override log to write to stderr so stdout can be used for return values
log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" >&2
}

AWS_REGION="${AWS_REGION:-us-east-1}"
ECR_REPO_PREFIX="${ECR_REPO_PREFIX:-nvsentinel-uat}"

# All NVSentinel images that need ECR repos
# Ko-built (Go modules)
KO_IMAGES=(
    "platform-connectors"
    "fault-quarantine"
    "fault-remediation"
    "node-drainer"
    "labeler"
    "janitor"
    "janitor-provider"
    "event-exporter"
    "kubernetes-object-monitor"
)

# Docker-built images (note: Makefiles add nvsentinel/ prefix to image path)
DOCKER_IMAGES=(
    "nvsentinel/gpu-health-monitor"
    "nvsentinel/syslog-health-monitor"
    "nvsentinel/log-collector"
    "nvsentinel/metadata-collector"
    "nvsentinel/file-server-cleanup"
)

# Combine all images
ALL_IMAGES=("${KO_IMAGES[@]}" "${DOCKER_IMAGES[@]}")

get_ecr_registry_base() {
    local account_id
    account_id=$(aws sts get-caller-identity --query Account --output text --region "$AWS_REGION")
    echo "${account_id}.dkr.ecr.${AWS_REGION}.amazonaws.com"
}

create_ecr_repos() {
    log "Creating ECR repositories with prefix: $ECR_REPO_PREFIX"
    log "Region: $AWS_REGION"
    
    local ecr_base
    ecr_base=$(get_ecr_registry_base)
    
    for image in "${ALL_IMAGES[@]}"; do
        local repo_name="${ECR_REPO_PREFIX}/${image}"
        
        # Check if repo already exists
        if aws ecr describe-repositories --repository-names "$repo_name" --region "$AWS_REGION" &>/dev/null; then
            log "  Repository already exists: $repo_name"
        else
            log "  Creating repository: $repo_name"
            if ! aws ecr create-repository \
                --repository-name "$repo_name" \
                --region "$AWS_REGION" \
                --image-scanning-configuration scanOnPush=false \
                --encryption-configuration encryptionType=AES256 \
                --output text >/dev/null; then
                error "Failed to create ECR repository: $repo_name"
            fi
        fi
    done
    
    log "ECR repositories created successfully ✓"
    log "Total repositories: ${#ALL_IMAGES[@]}"
    
    # Output the full registry path for use by other scripts
    echo "${ecr_base}/${ECR_REPO_PREFIX}"
}

main() {
    log "========================================="
    log "Creating ECR Repositories for NVSentinel"
    log "========================================="
    
    # Verify AWS credentials
    if ! aws sts get-caller-identity &>/dev/null; then
        error "AWS credentials not configured. Run 'aws configure' or set AWS environment variables."
    fi
    
    create_ecr_repos
}

main "$@"

