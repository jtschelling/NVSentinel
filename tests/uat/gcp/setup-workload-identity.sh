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

# Sets up GCP Workload Identity for the janitor-provider service account.
# This creates a GCP service account (if needed) and binds it to the 
# Kubernetes service account used by janitor-provider.
#
# Usage:
#   ./setup-workload-identity.sh
#
# Environment Variables:
#   GCP_PROJECT_ID      - GCP project ID (required)
#   GCP_SA_NAME         - Name for the GCP service account (default: nvsentinel-janitor-provider)
#   K8S_NAMESPACE       - Kubernetes namespace where janitor-provider runs (default: nvsentinel)
#   K8S_SA_NAME         - Kubernetes service account name (default: janitor-provider)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/../common.sh"

# Override log to write to stderr so stdout can be used for return values
log() {
    echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" >&2
}

GCP_PROJECT_ID="${GCP_PROJECT_ID:-}"
GCP_SA_NAME="${GCP_SA_NAME:-nvsentinel-janitor-provider}"
K8S_NAMESPACE="${K8S_NAMESPACE:-nvsentinel}"
K8S_SA_NAME="${K8S_SA_NAME:-janitor-provider}"

# Validate required inputs
if [[ -z "$GCP_PROJECT_ID" ]]; then
    error "GCP_PROJECT_ID must be set"
fi

GCP_SA_EMAIL="${GCP_SA_NAME}@${GCP_PROJECT_ID}.iam.gserviceaccount.com"

create_gcp_service_account() {
    log "Checking GCP service account..."
    
    # Check if service account already exists
    if gcloud iam service-accounts describe "$GCP_SA_EMAIL" \
        --project="$GCP_PROJECT_ID" &>/dev/null; then
        log "GCP service account already exists: $GCP_SA_NAME"
    else
        log "Creating GCP service account: $GCP_SA_NAME"
        if ! gcloud iam service-accounts create "$GCP_SA_NAME" \
            --project="$GCP_PROJECT_ID" \
            --display-name="NVSentinel Janitor Provider" \
            --description="Service account for NVSentinel janitor-provider to manage node reboots"; then
            error "Failed to create GCP service account"
        fi
        log "GCP service account created ✓"
    fi
}

grant_compute_permissions() {
    log "Granting compute.instanceAdmin.v1 role to service account..."
    
    # Check if binding already exists
    local existing_binding
    existing_binding=$(gcloud projects get-iam-policy "$GCP_PROJECT_ID" \
        --flatten="bindings[].members" \
        --filter="bindings.role:roles/compute.instanceAdmin.v1 AND bindings.members:serviceAccount:$GCP_SA_EMAIL" \
        --format="value(bindings.role)" 2>/dev/null || echo "")
    
    if [[ -n "$existing_binding" ]]; then
        log "IAM binding already exists"
    else
        log "Adding IAM policy binding..."
        # --condition=None is required when project has existing policies with conditions
        if ! gcloud projects add-iam-policy-binding "$GCP_PROJECT_ID" \
            --member="serviceAccount:$GCP_SA_EMAIL" \
            --role="roles/compute.instanceAdmin.v1" \
            --condition=None \
            --quiet; then
            error "Failed to add IAM policy binding"
        fi
        log "IAM policy binding added ✓"
    fi
}

setup_workload_identity_binding() {
    log "Setting up Workload Identity binding..."
    log "  GCP SA: $GCP_SA_EMAIL"
    log "  K8s SA: $K8S_NAMESPACE/$K8S_SA_NAME"
    
    local member="serviceAccount:${GCP_PROJECT_ID}.svc.id.goog[${K8S_NAMESPACE}/${K8S_SA_NAME}]"
    
    # Check if binding already exists
    local existing_binding
    existing_binding=$(gcloud iam service-accounts get-iam-policy "$GCP_SA_EMAIL" \
        --project="$GCP_PROJECT_ID" \
        --flatten="bindings[].members" \
        --filter="bindings.role:roles/iam.workloadIdentityUser AND bindings.members:$member" \
        --format="value(bindings.role)" 2>/dev/null || echo "")
    
    if [[ -n "$existing_binding" ]]; then
        log "Workload Identity binding already exists"
    else
        log "Creating Workload Identity binding..."
        # --condition=None is required when SA has existing policies with conditions
        if ! gcloud iam service-accounts add-iam-policy-binding "$GCP_SA_EMAIL" \
            --project="$GCP_PROJECT_ID" \
            --role="roles/iam.workloadIdentityUser" \
            --member="$member" \
            --condition=None \
            --quiet; then
            error "Failed to create Workload Identity binding"
        fi
        log "Workload Identity binding created ✓"
    fi
}

main() {
    log "========================================="
    log "Setting up GCP Workload Identity"
    log "========================================="
    log "Project: $GCP_PROJECT_ID"
    log "GCP Service Account: $GCP_SA_NAME"
    log "K8s Namespace: $K8S_NAMESPACE"
    log "K8s Service Account: $K8S_SA_NAME"
    log "========================================="
    
    # Verify gcloud is authenticated
    if ! gcloud auth print-access-token &>/dev/null; then
        error "gcloud is not authenticated. Run 'gcloud auth login' or configure service account."
    fi
    
    create_gcp_service_account
    grant_compute_permissions
    setup_workload_identity_binding
    
    log "========================================="
    log "Workload Identity Setup Complete ✓"
    log "========================================="
    log ""
    log "The janitor-provider K8s service account can now use the GCP service account:"
    log "  GCP SA: $GCP_SA_EMAIL"
    log ""
    log "Helm values to use:"
    log "  janitor-provider.csp.provider: gcp"
    log "  janitor-provider.csp.gcp.project: $GCP_PROJECT_ID"
    log "  janitor-provider.csp.gcp.serviceAccount: $GCP_SA_NAME"
    log ""
    
    # Output the service account name for use by other scripts
    echo "$GCP_SA_NAME"
}

main "$@"

