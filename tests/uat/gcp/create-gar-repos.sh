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

# Creates a Google Artifact Registry repository for NVSentinel images.
# Outputs the GAR registry URL to stdout for use by other scripts.
#
# Usage:
#   ./create-gar-repos.sh
#   GAR_REGISTRY=$(./create-gar-repos.sh)
#
# Environment Variables:
#   GCP_PROJECT_ID  - GCP project ID (required)
#   GCP_REGION      - GCP region for the repository (default: us-central1)
#   GAR_REPO_NAME   - Name of the Artifact Registry repository (default: nvsentinel-uat)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

# Override log to write to stderr so stdout can be used for return values
log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" >&2
}

GCP_PROJECT_ID="${GCP_PROJECT_ID:-}"
GCP_REGION="${GCP_REGION:-us-central1}"
GAR_REPO_NAME="${GAR_REPO_NAME:-nvsentinel-uat}"

# Validate required inputs
if [[ -z "$GCP_PROJECT_ID" ]]; then
    error "GCP_PROJECT_ID must be set"
fi

get_gar_registry() {
    echo "${GCP_REGION}-docker.pkg.dev/${GCP_PROJECT_ID}/${GAR_REPO_NAME}"
}

create_gar_repo() {
    log "Creating Artifact Registry repository..."
    log "  Project: $GCP_PROJECT_ID"
    log "  Region: $GCP_REGION"
    log "  Repository: $GAR_REPO_NAME"
    
    local gar_registry
    gar_registry=$(get_gar_registry)
    
    # Check if repository already exists
    if gcloud artifacts repositories describe "$GAR_REPO_NAME" \
        --project="$GCP_PROJECT_ID" \
        --location="$GCP_REGION" &>/dev/null; then
        log "Repository already exists: $GAR_REPO_NAME"
    else
        log "Creating repository: $GAR_REPO_NAME"
        if ! gcloud artifacts repositories create "$GAR_REPO_NAME" \
            --project="$GCP_PROJECT_ID" \
            --location="$GCP_REGION" \
            --repository-format=docker \
            --description="NVSentinel UAT images" \
            --quiet; then
            error "Failed to create Artifact Registry repository"
        fi
    fi
    
    log "Artifact Registry repository ready ✓"
    log "Registry URL: $gar_registry"
    
    # Output the registry URL for use by other scripts
    echo "$gar_registry"
}

main() {
    log "========================================="
    log "Creating Artifact Registry for NVSentinel"
    log "========================================="
    
    # Verify gcloud is authenticated
    if ! gcloud auth print-access-token &>/dev/null; then
        error "gcloud is not authenticated. Run 'gcloud auth login' or configure service account."
    fi
    
    # Ensure Artifact Registry API is enabled
    log "Ensuring Artifact Registry API is enabled..."
    if ! gcloud services enable artifactregistry.googleapis.com \
        --project="$GCP_PROJECT_ID" --quiet; then
        log "WARNING: Could not enable Artifact Registry API (may already be enabled)"
    fi
    
    create_gar_repo
}

main "$@"

