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

# Deletes all ECR repositories with the specified prefix.
# Safe to run multiple times (idempotent).
#
# Usage:
#   ./delete-ecr-repos.sh
#   ECR_REPO_PREFIX=my-prefix ./delete-ecr-repos.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

AWS_REGION="${AWS_REGION:-us-east-1}"
ECR_REPO_PREFIX="${ECR_REPO_PREFIX:-nvsentinel-uat}"

delete_ecr_repos() {
    log "Deleting ECR repositories with prefix: $ECR_REPO_PREFIX"
    log "Region: $AWS_REGION"
    
    # List all repos with our prefix
    local repos
    repos=$(aws ecr describe-repositories \
        --region "$AWS_REGION" \
        --query "repositories[?starts_with(repositoryName, '${ECR_REPO_PREFIX}/')].repositoryName" \
        --output text 2>/dev/null || echo "")
    
    if [[ -z "$repos" ]]; then
        log "No ECR repositories found with prefix: $ECR_REPO_PREFIX"
        return 0
    fi
    
    local count=0
    for repo in $repos; do
        log "  Deleting repository: $repo"
        if aws ecr delete-repository \
            --repository-name "$repo" \
            --region "$AWS_REGION" \
            --force >/dev/null 2>&1; then
            ((count++))
        else
            log "  WARNING: Failed to delete $repo"
        fi
    done
    
    log "ECR repositories deleted: $count ✓"
}

main() {
    log "========================================="
    log "Deleting ECR Repositories"
    log "========================================="
    
    # Verify AWS credentials
    if ! aws sts get-caller-identity &>/dev/null; then
        error "AWS credentials not configured. Run 'aws configure' or set AWS environment variables."
    fi
    
    delete_ecr_repos
    
    log "ECR cleanup complete ✓"
}

main "$@"

