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

# Creates a GKE cluster using Terraform and configures kubectl credentials.
#
# Required Environment Variables:
#   TF_VAR_project_id     - GCP project ID
#   TF_VAR_deployment_id  - Unique deployment identifier (e.g., "d12345")
#   TF_VAR_zone           - GCP zone (e.g., "europe-west4-b")
#
# Optional Environment Variables (with defaults in variables.tf):
#   TF_VAR_region, TF_VAR_system_node_type, TF_VAR_system_node_count,
#   TF_VAR_gpu_machine_type, TF_VAR_gpu_node_count, etc.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

# Cluster naming prefix (matches workflow)
PREFIX="${PREFIX:-nvs}"

# Validate required environment variables
if [[ -z "${TF_VAR_project_id:-}" ]]; then
    error "TF_VAR_project_id must be set"
fi

if [[ -z "${TF_VAR_deployment_id:-}" ]]; then
    error "TF_VAR_deployment_id must be set"
fi

if [[ -z "${TF_VAR_zone:-}" ]]; then
    error "TF_VAR_zone must be set"
fi

CLUSTER_NAME="${PREFIX}-${TF_VAR_deployment_id}"

create_cluster() {
    log "Creating GKE cluster with Terraform..."
    log "  Project: ${TF_VAR_project_id}"
    log "  Zone: ${TF_VAR_zone}"
    log "  Cluster Name: ${CLUSTER_NAME}"
    
    cd "${SCRIPT_DIR}/cluster"
    
    log "Initializing Terraform..."
    if ! terraform init; then
        error "Failed to initialize Terraform"
    fi
    
    log "Applying Terraform configuration..."
    if ! terraform apply -auto-approve; then
        error "Failed to create GKE cluster"
    fi
    
    log "GKE cluster created successfully ✓"
}

connect_to_cluster() {
    log "Configuring kubectl credentials..."
    
    # Install GKE auth plugin if needed
    log "Ensuring GKE auth plugin is installed..."
    gcloud components install gke-gcloud-auth-plugin --quiet --project "${TF_VAR_project_id}" 2>/dev/null || true
    
    # Get cluster credentials
    log "Getting cluster credentials..."
    if ! gcloud container clusters get-credentials "${CLUSTER_NAME}" \
        --zone "${TF_VAR_zone}" \
        --project "${TF_VAR_project_id}"; then
        error "Failed to get cluster credentials"
    fi
    
    # Verify connection
    log "Verifying cluster connection..."
    if ! kubectl cluster-info; then
        error "Failed to connect to cluster"
    fi
    
    log "Connected to GKE cluster ✓"
}

main() {
    log "========================================="
    log "Creating GKE Cluster"
    log "========================================="
    
    # Verify prerequisites
    if ! command -v terraform &> /dev/null; then
        error "terraform is required but not installed"
    fi
    
    if ! command -v gcloud &> /dev/null; then
        error "gcloud is required but not installed"
    fi
    
    if ! gcloud auth print-access-token &> /dev/null; then
        error "gcloud is not authenticated. Run 'gcloud auth login' or configure service account."
    fi
    
    create_cluster
    connect_to_cluster
    
    log "========================================="
    log "GKE Cluster Ready"
    log "========================================="
    log "Cluster: ${CLUSTER_NAME}"
    log "Project: ${TF_VAR_project_id}"
    log "Zone: ${TF_VAR_zone}"
}

main "$@"

