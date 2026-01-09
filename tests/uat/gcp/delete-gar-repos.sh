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

# Deletes the Google Artifact Registry repository used for NVSentinel images.
# Safe to run multiple times (idempotent).
#
# Usage:
#   ./delete-gar-repos.sh
#   GCP_PROJECT_ID=my-project ./delete-gar-repos.sh
#
# Environment Variables:
#   GCP_PROJECT_ID  - GCP project ID (required)
#   GCP_REGION      - GCP region for the repository (default: us-central1)
#   GAR_REPO_NAME   - Name of the Artifact Registry repository (default: nvsentinel-uat)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

GCP_PROJECT_ID="${GCP_PROJECT_ID:-}"
GCP_REGION="${GCP_REGION:-us-central1}"
GAR_REPO_NAME="${GAR_REPO_NAME:-nvsentinel-uat}"

# Validate required inputs
if [[ -z "$GCP_PROJECT_ID" ]]; then
    error "GCP_PROJECT_ID must be set"
fi

delete_gar_repo() {
    log "Deleting Artifact Registry repository..."
    log "  Project: $GCP_PROJECT_ID"
    log "  Region: $GCP_REGION"
    log "  Repository: $GAR_REPO_NAME"
    
    # Check if repository exists
    if ! gcloud artifacts repositories describe "$GAR_REPO_NAME" \
        --project="$GCP_PROJECT_ID" \
        --location="$GCP_REGION" &>/dev/null; then
        log "Repository does not exist: $GAR_REPO_NAME (nothing to delete)"
        return 0
    fi
    
    # Delete the repository
    log "Deleting repository: $GAR_REPO_NAME"
    if ! gcloud artifacts repositories delete "$GAR_REPO_NAME" \
        --project="$GCP_PROJECT_ID" \
        --location="$GCP_REGION" \
        --quiet; then
        log "WARNING: Failed to delete repository: $GAR_REPO_NAME"
        return 1
    fi
    
    log "Artifact Registry repository deleted ✓"
}

main() {
    log "========================================="
    log "Deleting Artifact Registry Repository"
    log "========================================="
    
    # Verify gcloud is authenticated
    if ! gcloud auth print-access-token &>/dev/null; then
        error "gcloud is not authenticated. Run 'gcloud auth login' or configure service account."
    fi
    
    delete_gar_repo
}

main "$@"

