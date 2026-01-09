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


set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/common.sh"

REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
VERSIONS_FILE="${REPO_ROOT}/.versions.yaml"

# Verify yq is available for parsing versions
if ! command -v yq &> /dev/null; then
    error "yq is required but not installed. Please install yq: https://github.com/mikefarah/yq"
fi

# Load versions from .versions.yaml that are exported to install-apps.sh
PROMETHEUS_OPERATOR_VERSION=$(yq eval '.cluster.prometheus_operator' "$VERSIONS_FILE")
GPU_OPERATOR_VERSION=$(yq eval '.cluster.gpu_operator' "$VERSIONS_FILE")
CERT_MANAGER_VERSION=$(yq eval '.cluster.cert_manager' "$VERSIONS_FILE")

# Validate required versions were loaded
if [[ -z "$PROMETHEUS_OPERATOR_VERSION" ]] || [[ -z "$GPU_OPERATOR_VERSION" ]] || [[ -z "$CERT_MANAGER_VERSION" ]]; then
    error "Failed to load required versions from $VERSIONS_FILE. Please verify the file exists and contains required keys."
fi

# Note: KWOK versions are loaded directly by install-apps.sh from .versions.yaml

# Configuration
CSP="${CSP:-kind}"  # Default to kind for local development
CLUSTER_NAME="${CLUSTER_NAME:-nvsentinel-uat}"
AWS_REGION="${AWS_REGION:-us-east-1}"
K8S_VERSION="${K8S_VERSION:-1.34}"
GPU_AVAILABILITY_ZONE="${GPU_AVAILABILITY_ZONE:-e}"
CPU_NODE_TYPE="${CPU_NODE_TYPE:-m7a.4xlarge}"
CPU_NODE_COUNT="${CPU_NODE_COUNT:-3}"
GPU_NODE_TYPE="${GPU_NODE_TYPE:-p5.48xlarge}"
GPU_NODE_COUNT="${GPU_NODE_COUNT:-2}"
CAPACITY_RESERVATION_ID="${CAPACITY_RESERVATION_ID:-}"

# NVSentinel configuration
NVSENTINEL_VERSION="${NVSENTINEL_VERSION:-}"
NVSENTINEL_TAG="${NVSENTINEL_TAG:-main}"

# Local build configuration (USE_LOCAL_BUILD=true to build images locally and push to registry)
USE_LOCAL_BUILD="${USE_LOCAL_BUILD:-false}"

# AWS ECR configuration (for USE_LOCAL_BUILD with CSP=aws)
ECR_REPO_PREFIX="${ECR_REPO_PREFIX:-nvsentinel-uat}"
ECR_VALUES_FILE="${ECR_VALUES_FILE:-/tmp/nvsentinel-ecr-values.yaml}"
ECR_REGISTRY=""  # Set by setup_ecr_and_build()

# GCP Artifact Registry configuration (for USE_LOCAL_BUILD with CSP=gcp)
GCP_PROJECT_ID="${GCP_PROJECT_ID:-}"
GCP_REGION="${GCP_REGION:-us-central1}"
GCP_ZONE="${GCP_ZONE:-}"
GCP_SERVICE_ACCOUNT="${GCP_SERVICE_ACCOUNT:-}"
GAR_REPO_NAME="${GAR_REPO_NAME:-nvsentinel-uat}"
GAR_VALUES_FILE="${GAR_VALUES_FILE:-/tmp/nvsentinel-gar-values.yaml}"
GAR_REGISTRY=""  # Set by setup_gar_and_build()

DELETE_CLUSTER_ON_EXIT="${DELETE_CLUSTER_ON_EXIT:-false}"

cleanup() {
    if [[ "$DELETE_CLUSTER_ON_EXIT" == "true" ]]; then
        log "========================================="
        log "Cleaning up cluster..."
        log "========================================="
        
        # Skip cleanup for Kind clusters (managed externally)
        if [[ "$CSP" == "kind" ]]; then
            log "Kind cluster cleanup skipped (managed externally)"
            return 0
        fi
        
        local original_dir="$PWD"
        
        export CLUSTER_NAME
        export AWS_REGION
        
        # GCP-specific exports for cleanup
        if [[ "$CSP" == "gcp" ]]; then
            export TF_VAR_project_id="${GCP_PROJECT_ID}"
            export TF_VAR_deployment_id
            export TF_VAR_zone="${GCP_ZONE}"
        fi
        
        # Get the cluster deletion script for this CSP
        local delete_script
        delete_script=$(get_cluster_script "delete")
        
        cd "${SCRIPT_DIR}/${CSP}"
        "./${delete_script}" || log "WARNING: Cluster deletion failed"
        cd "$original_dir"
        
        # Clean up container registry if using local build
        if [[ "$USE_LOCAL_BUILD" == "true" ]]; then
            if [[ "$CSP" == "aws" ]]; then
                log "Cleaning up ECR repositories..."
                export ECR_REPO_PREFIX
                "${SCRIPT_DIR}/${CSP}/delete-ecr-repos.sh" || log "WARNING: ECR cleanup failed"
            elif [[ "$CSP" == "gcp" ]]; then
                log "Cleaning up GAR repository..."
                export GCP_PROJECT_ID
                export GCP_REGION
                export GAR_REPO_NAME
                "${SCRIPT_DIR}/${CSP}/delete-gar-repos.sh" || log "WARNING: GAR cleanup failed"
            fi
        fi
        
        log "Cluster cleanup completed"
    fi
}

trap cleanup EXIT

check_prerequisites() {
    log "Checking prerequisites..."
    
    if ! command -v kubectl &> /dev/null; then
        error "kubectl is not installed"
    fi
    
    if ! command -v helm &> /dev/null; then
        error "helm is not installed. Install from: https://helm.sh/docs/intro/install/"
    fi
    
    if [[ "$CSP" == "aws" ]]; then
        if ! command -v aws &> /dev/null; then
            error "aws CLI is not installed. Install from: https://aws.amazon.com/cli/"
        fi
        
        if ! command -v eksctl &> /dev/null; then
            error "eksctl is not installed. Install from: https://eksctl.io/installation/"
        fi
        
        if ! command -v envsubst &> /dev/null; then
            error "envsubst is not installed. Install gettext package."
        fi
        
        if ! aws sts get-caller-identity &> /dev/null; then
            error "AWS credentials not configured. Run 'aws configure' or set AWS environment variables."
        fi
        
        if [[ -z "$CAPACITY_RESERVATION_ID" ]]; then
            error "CAPACITY_RESERVATION_ID is required for AWS. Set it via environment variable."
        fi
    fi
    
    if [[ "$CSP" == "gcp" ]]; then
        if ! command -v gcloud &> /dev/null; then
            error "gcloud CLI is not installed. Install from: https://cloud.google.com/sdk/docs/install"
        fi
        
        if ! command -v terraform &> /dev/null; then
            error "terraform is not installed. Install from: https://www.terraform.io/downloads"
        fi
        
        if ! gcloud auth print-access-token &> /dev/null; then
            error "gcloud is not authenticated. Run 'gcloud auth login' or configure service account."
        fi
        
        if [[ -z "$GCP_PROJECT_ID" ]]; then
            error "GCP_PROJECT_ID is required for GCP. Set it via environment variable."
        fi
        
        if [[ -z "$GCP_ZONE" ]]; then
            error "GCP_ZONE is required for GCP. Set it via environment variable."
        fi
    fi
    
    # Check for USE_LOCAL_BUILD prerequisites
    if [[ "$USE_LOCAL_BUILD" == "true" ]]; then
        if [[ "$CSP" != "aws" ]] && [[ "$CSP" != "gcp" ]]; then
            error "USE_LOCAL_BUILD is currently only supported for CSP=aws or CSP=gcp"
        fi
        
        if ! command -v ko &> /dev/null; then
            error "ko is required for USE_LOCAL_BUILD but not installed. Install from: https://ko.build/install/"
        fi
        
        if ! command -v docker &> /dev/null; then
            error "docker is required for USE_LOCAL_BUILD but not installed"
        fi
        
        # GCP-specific checks
        if [[ "$CSP" == "gcp" ]]; then
            if ! command -v gcloud &> /dev/null; then
                error "gcloud CLI is required for USE_LOCAL_BUILD on GCP. Install from: https://cloud.google.com/sdk/docs/install"
            fi
            
            if [[ -z "$GCP_PROJECT_ID" ]]; then
                error "GCP_PROJECT_ID is required for USE_LOCAL_BUILD on GCP"
            fi
            
            if ! gcloud auth print-access-token &> /dev/null; then
                error "gcloud is not authenticated. Run 'gcloud auth login' or configure service account."
            fi
            
            log "Using local build mode - images will be built and pushed to GAR"
        else
            log "Using local build mode - images will be built and pushed to ECR"
        fi
    elif [[ -z "$NVSENTINEL_VERSION" ]]; then
        error "NVSENTINEL_VERSION is required. Set it via environment variable: export NVSENTINEL_VERSION='v0.1.0' or use USE_LOCAL_BUILD=true"
    fi
    
    local values_dir="${SCRIPT_DIR}/${CSP}"
    if [[ ! -f "${values_dir}/prometheus-operator-values.yaml" ]]; then
        error "Prometheus values file not found: ${values_dir}/prometheus-operator-values.yaml"
    fi
    
    if [[ ! -f "${values_dir}/gpu-operator-values.yaml" ]]; then
        error "GPU Operator values file not found: ${values_dir}/gpu-operator-values.yaml"
    fi
    
    if [[ ! -f "${values_dir}/cert-manager-values.yaml" ]]; then
        error "cert-manager values file not found: ${values_dir}/cert-manager-values.yaml"
    fi
    
    if [[ ! -f "${values_dir}/nvsentinel-values.yaml" ]]; then
        error "NVSentinel values file not found: ${values_dir}/nvsentinel-values.yaml"
    fi
    
    local nvsentinel_chart="${SCRIPT_DIR}/../../distros/kubernetes/nvsentinel"
    if [[ ! -d "$nvsentinel_chart" ]]; then
        error "NVSentinel chart not found: $nvsentinel_chart"
    fi
    
    log "Prerequisites check passed ✓"
}

# Get the cluster management script name for the specified CSP
# Args: $1 - operation type ("create" or "delete")
# Returns: script name via stdout, or calls error() for unsupported CSPs
get_cluster_script() {
    local operation="$1"
    
    case "$CSP" in
        kind)
            # Kind clusters are managed externally - this function should not be called for Kind
            error "get_cluster_script() should not be called for CSP=kind (clusters are managed externally)"
            ;;
        aws)
            if [[ "$operation" == "create" ]]; then
                echo "create-eks-cluster.sh"
            else
                echo "delete-eks-cluster.sh"
            fi
            ;;
        gcp)
            if [[ "$operation" == "create" ]]; then
                echo "create-gke-cluster.sh"
            else
                echo "delete-gke-cluster.sh"
            fi
            ;;
        azure|oci)
            error "CSP '$CSP' cluster $operation not yet implemented. Please manage cluster manually or use CSP=aws, CSP=gcp, or CSP=kind"
            ;;
        *)
            error "Unknown CSP: $CSP. Supported values: kind, aws, gcp, azure, oci"
            ;;
    esac
}

# Set up ECR and build images locally (for USE_LOCAL_BUILD mode on AWS)
setup_ecr_and_build() {
    log "========================================="
    log "Setting up ECR and building images..."
    log "========================================="
    
    export AWS_REGION
    export ECR_REPO_PREFIX
    export ECR_VALUES_FILE
    
    # Create ECR repositories and capture the registry URL
    log "Creating ECR repositories..."
    ECR_REGISTRY=$("${SCRIPT_DIR}/${CSP}/create-ecr-repos.sh")
    export ECR_REGISTRY
    
    log "ECR Registry: $ECR_REGISTRY"
    
    # Build and push images
    log "Building and pushing images to ECR..."
    "${SCRIPT_DIR}/${CSP}/build-push-images.sh"
    
    # Set NVSENTINEL_VERSION to the git SHA used for image tags
    NVSENTINEL_VERSION=$(git -C "$REPO_ROOT" rev-parse --short HEAD)
    export NVSENTINEL_VERSION
    
    log "ECR setup and image build complete ✓"
    log "Image Tag: $NVSENTINEL_VERSION"
    log "ECR Values File: $ECR_VALUES_FILE"
}

# Set up GAR and build images locally (for USE_LOCAL_BUILD mode on GCP)
setup_gar_and_build() {
    log "========================================="
    log "Setting up GAR and building images..."
    log "========================================="
    
    export GCP_PROJECT_ID
    export GCP_REGION
    export GAR_REPO_NAME
    export GAR_VALUES_FILE
    
    # Create GAR repository and capture the registry URL
    log "Creating Artifact Registry repository..."
    GAR_REGISTRY=$("${SCRIPT_DIR}/${CSP}/create-gar-repos.sh")
    export GAR_REGISTRY
    
    log "GAR Registry: $GAR_REGISTRY"
    
    # Set up workload identity for janitor-provider
    log "Setting up Workload Identity for janitor-provider..."
    GCP_SERVICE_ACCOUNT=$("${SCRIPT_DIR}/${CSP}/setup-workload-identity.sh")
    export GCP_SERVICE_ACCOUNT
    
    # Build and push images
    log "Building and pushing images to GAR..."
    "${SCRIPT_DIR}/${CSP}/build-push-images.sh"
    
    # Set NVSENTINEL_VERSION to the git SHA used for image tags
    NVSENTINEL_VERSION=$(git -C "$REPO_ROOT" rev-parse --short HEAD)
    export NVSENTINEL_VERSION
    
    # Export the GAR values file path as ECR_VALUES_FILE for install-apps.sh compatibility
    ECR_VALUES_FILE="$GAR_VALUES_FILE"
    export ECR_VALUES_FILE
    
    log "GAR setup and image build complete ✓"
    log "Image Tag: $NVSENTINEL_VERSION"
    log "GAR Values File: $GAR_VALUES_FILE"
}

create_cluster() {
    log "========================================="
    log "Creating ${CSP} cluster..."
    log "========================================="
    
    # Kind clusters are assumed to be created externally
    if [[ "$CSP" == "kind" ]]; then
        log "Using existing Kind cluster: $CLUSTER_NAME"
        
        # Verify cluster exists
        if ! kubectl cluster-info --context "kind-$CLUSTER_NAME" &> /dev/null; then
            error "Kind cluster '$CLUSTER_NAME' not found. Create it first with: kind create cluster --name $CLUSTER_NAME"
        fi
        
        # Switch to the Kind cluster context
        log "Switching to Kind cluster context..."
        if ! kubectl config use-context "kind-$CLUSTER_NAME" &> /dev/null; then
            error "Failed to switch to Kind cluster context: kind-$CLUSTER_NAME"
        fi
        
        log "Kind cluster verified and context set ✓"
        return 0
    fi
    
    # For cloud CSPs, run the cluster creation script
    export CLUSTER_NAME
    
    # AWS-specific exports
    if [[ "$CSP" == "aws" ]]; then
        export AWS_REGION
        export K8S_VERSION
        export GPU_AVAILABILITY_ZONE
        export CPU_NODE_TYPE
        export CPU_NODE_COUNT
        export GPU_NODE_TYPE
        export GPU_NODE_COUNT
        export CAPACITY_RESERVATION_ID
    fi
    
    # GCP-specific exports (Terraform variables)
    if [[ "$CSP" == "gcp" ]]; then
        # Required Terraform variables
        export TF_VAR_project_id="${GCP_PROJECT_ID}"
        export TF_VAR_deployment_id="${TF_VAR_deployment_id:-d$(date +%s)}"
        export TF_VAR_zone="${GCP_ZONE}"
        export TF_VAR_region="${GCP_REGION}"
        
        # Optional: can be overridden via environment
        export TF_VAR_system_node_type="${TF_VAR_system_node_type:-e2-standard-4}"
        export TF_VAR_system_node_count="${TF_VAR_system_node_count:-3}"
    fi
    
    # Get the cluster creation script for this CSP
    local cluster_script
    cluster_script=$(get_cluster_script "create")
    
    cd "${SCRIPT_DIR}/${CSP}"
    "./${cluster_script}"
    cd "${SCRIPT_DIR}"
    
    log "Cluster created successfully ✓"
}

install_apps() {
    log "========================================="
    log "Installing applications..."
    log "========================================="
    
    export CLUSTER_NAME
    export AWS_REGION
    export CSP
    export PROMETHEUS_OPERATOR_VERSION
    export GPU_OPERATOR_VERSION
    export CERT_MANAGER_VERSION
    export NVSENTINEL_VERSION
    
    # Export GCP variables for janitor-provider configuration
    if [[ "$CSP" == "gcp" ]]; then
        export GCP_PROJECT_ID
        export GCP_ZONE
        export GCP_SERVICE_ACCOUNT
    fi
    
    # Export registry values file if using local build
    if [[ "$USE_LOCAL_BUILD" == "true" ]]; then
        export ECR_VALUES_FILE  # Works for both ECR and GAR (GAR sets this to GAR_VALUES_FILE)
    fi
    
    ./install-apps.sh
    
    log "Applications installed successfully ✓"
}

run_tests() {
    log "========================================="
    log "Running UAT tests..."
    log "========================================="
    
    ./tests.sh
    
    log "All tests passed ✓"
}

main() {
    log "========================================="
    log "NVSentinel UAT Test Suite"
    log "========================================="
    log "CSP: $CSP"
    log "Cluster: $CLUSTER_NAME"
    if [[ "$USE_LOCAL_BUILD" == "true" ]]; then
        if [[ "$CSP" == "aws" ]]; then
            log "Build Mode: Local (ECR)"
            log "ECR Repo Prefix: $ECR_REPO_PREFIX"
        elif [[ "$CSP" == "gcp" ]]; then
            log "Build Mode: Local (GAR)"
            log "GCP Project: $GCP_PROJECT_ID"
            log "GCP Region: $GCP_REGION"
            log "GAR Repo Name: $GAR_REPO_NAME"
        fi
    else
        log "NVSentinel Version: $NVSENTINEL_VERSION"
    fi
    log "Delete on Exit: $DELETE_CLUSTER_ON_EXIT"
    log "========================================="
    
    check_prerequisites
    
    # Build images first if using local build mode
    if [[ "$USE_LOCAL_BUILD" == "true" ]]; then
        if [[ "$CSP" == "aws" ]]; then
            setup_ecr_and_build
        elif [[ "$CSP" == "gcp" ]]; then
            setup_gar_and_build
        fi
    fi
    
    create_cluster
    install_apps
    run_tests
    
    log "========================================="
    log "UAT Test Suite Completed Successfully! ✓"
    log "========================================="
}

main "$@"
