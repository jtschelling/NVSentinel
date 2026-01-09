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

# Destroys a GKE cluster using Terraform.
#
# Required Environment Variables:
#   TF_VAR_project_id     - GCP project ID
#   TF_VAR_deployment_id  - Unique deployment identifier (must match create)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

delete_cluster() {
    log "Destroying GKE cluster with Terraform..."
    
    cd "${SCRIPT_DIR}/cluster"
    
    # Check if terraform state exists
    if [[ ! -f "terraform.tfstate" ]] && [[ ! -d ".terraform" ]]; then
        log "No Terraform state found - cluster may not exist or was created elsewhere"
        return 0
    fi
    
    log "Running terraform destroy..."
    if ! terraform destroy -auto-approve; then
        log "WARNING: Terraform destroy failed"
        return 1
    fi
    
    log "GKE cluster destroyed successfully ✓"
}

main() {
    log "========================================="
    log "Destroying GKE Cluster"
    log "========================================="
    
    # Verify prerequisites
    if ! command -v terraform &> /dev/null; then
        error "terraform is required but not installed"
    fi
    
    delete_cluster
    
    log "GKE cluster cleanup complete"
}

main "$@"

