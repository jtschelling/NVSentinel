// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package crstatus

import (
	"context"
	"log/slog"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nvidia/nvsentinel/fault-remediation/pkg/config"
)

type CRStatusChecker struct {
	client             client.Client
	remediationActions map[string]config.MaintenanceResource
	dryRun             bool
}

type CRState string

const (
	CRStateNotFound   CRState = "NotFound"
	CRStateInProgress CRState = "InProgress"
	CRStateSucceeded  CRState = "Succeeded"
	CRStateFailed     CRState = "Failed"
)

func NewCRStatusChecker(
	client client.Client,
	remediationActions map[string]config.MaintenanceResource,
	dryRun bool,
) *CRStatusChecker {
	return &CRStatusChecker{
		client:             client,
		remediationActions: remediationActions,
		dryRun:             dryRun,
	}
}

// ShouldSkipCRCreation returns true if an existing CR should suppress creation of a new CR.
func (c *CRStatusChecker) ShouldSkipCRCreation(ctx context.Context, actionName string, crName string) bool {
	state := c.GetCRState(ctx, actionName, crName)
	return state == CRStateInProgress || state == CRStateSucceeded
}

func (c *CRStatusChecker) GetCRState(ctx context.Context, actionName string, crName string) CRState {
	resource, exists := c.remediationActions[actionName]
	if !exists {
		slog.ErrorContext(ctx, "No remediation configuration found for action", "action", actionName)
		return CRStateNotFound
	}

	if c.dryRun {
		slog.InfoContext(ctx, "DRY-RUN: CR doesn't exist (dry-run mode)", "crName", crName, "action", actionName)
		return CRStateNotFound
	}

	gvk := schema.GroupVersionKind{
		Group:   resource.ApiGroup,
		Version: resource.Version,
		Kind:    resource.Kind,
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	key := client.ObjectKey{Name: crName, Namespace: resource.Namespace}

	if err := c.client.Get(ctx, key, obj); err != nil {
		slog.WarnContext(ctx, "Failed to get CR, allowing create", "crName", crName, "gvk", gvk.String(), "error", err)
		return CRStateNotFound
	}

	return c.checkCondition(obj, resource)
}

func (c *CRStatusChecker) checkCondition(obj *unstructured.Unstructured, resource config.MaintenanceResource) CRState {
	status, found, err := unstructured.NestedMap(obj.Object, "status")
	if err != nil || !found {
		// Fresh-created CR (status not yet populated). For ERR the spec treats
		// this as "no claim yet" so dedup doesn't block on an uninitialized
		// object; the deterministic-hash CR name still prevents accidental
		// duplicates if a create is retried.
		if isExternalRemediationRequest(resource) {
			return CRStateNotFound
		}

		return CRStateInProgress
	}

	conditions, found, err := unstructured.NestedSlice(status, "conditions")
	if err != nil || !found {
		if isExternalRemediationRequest(resource) {
			return CRStateNotFound
		}

		return CRStateInProgress
	}

	conditionStatus := c.findConditionStatus(conditions, resource.CompleteConditionType)

	if isExternalRemediationRequest(resource) {
		return errStateFromCondition(conditionStatus)
	}

	switch conditionStatus {
	case "True":
		return CRStateSucceeded
	case "False":
		return CRStateFailed
	default:
		return CRStateInProgress
	}
}

// ExternalRemediationRequest constants mirror the values used by the ERR
// reconciler and the fault-remediation TOML config entry. The pair is the
// dispatch key for the asymmetric True/False semantics defined in ADR-040.
const (
	errAPIGroup = "nvsentinel.nvidia.com"
	errKind     = "ExternalRemediationRequest"
)

// isExternalRemediationRequest reports whether the configured remediation
// resource targets the ERR CRD. ERR has asymmetric completion semantics
// per ADR-040, so checkCondition routes it through errStateFromCondition
// rather than the default True=Succeeded / False=Failed mapping.
func isExternalRemediationRequest(r config.MaintenanceResource) bool {
	return r.ApiGroup == errAPIGroup && r.Kind == errKind
}

// errStateFromCondition implements the ADR-040 asymmetric mapping for the
// ExternalRemediationComplete condition:
//
//   - "True"   -> external system reported success; ERR reconciler is
//     unwinding the release taint and managed=false label. The
//     equivalence-group entry should be pruned and a new fault on the same
//     node is free to create another ERR. -> CRStateNotFound.
//   - "False"  -> external system reported failure / gave up. The node
//     remains released and the ERR remains the active claim until an
//     operator deletes the ERR or the external system retries with True.
//     -> CRStateInProgress (suppress duplicate creation).
//   - "Unknown" -> in-flight; release taint may or may not be on the node
//     yet. -> CRStateInProgress.
//   - missing  -> ERR exists but its status is empty (race with the
//     reconciler's first reconcile). Spec calls this out: treat as "no
//     claim" so dedup doesn't lock on a half-initialised ERR. The
//     deterministic-hash CR name keeps creation idempotent.
//     -> CRStateNotFound.
func errStateFromCondition(conditionStatus string) CRState {
	switch conditionStatus {
	case "True", "":
		return CRStateNotFound
	case "False", "Unknown":
		return CRStateInProgress
	default:
		// Any unrecognised value (defensive): treat as in-flight.
		return CRStateInProgress
	}
}

func (c *CRStatusChecker) findConditionStatus(conditions []any, completeConditionType string) string {
	for _, cond := range conditions {
		condition, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}

		condType, _ := condition["type"].(string)
		if condType == completeConditionType {
			condStatus, _ := condition["status"].(string)
			return condStatus
		}
	}

	return ""
}
