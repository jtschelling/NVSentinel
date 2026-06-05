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

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	nvsentinelv1 "github.com/nvidia/nvsentinel/janitor/api/v1alpha1"
	"github.com/nvidia/nvsentinel/janitor/pkg/condition"
)

const (
	// ExternalRemediationFinalizer is added to every ExternalRemediationRequest
	// so that node cleanup is guaranteed to run when the ERR is deleted. Per
	// ADR-040 this is the only mechanism by which operators reclaim a node
	// held by a stalled or failed external system (`kubectl delete err <n>`).
	ExternalRemediationFinalizer = "nvsentinel.nvidia.com/external-remediation-cleanup"

	// ConditionNVSentinelOwnershipReleased is set True by the reconciler once
	// the release taint and `managed=false` label have been applied to the
	// target Node. Until then it remains Unknown.
	ConditionNVSentinelOwnershipReleased = "NVSentinelOwnershipReleased"

	// ConditionExternalRemediationComplete is set by the external system to
	// signal completion. True triggers cleanup; False leaves the node released
	// (asymmetric — see ADR-040).
	ConditionExternalRemediationComplete = "ExternalRemediationComplete"

	// reasonInitializing is the initial reason for NVSentinelOwnershipReleased.
	reasonInitializing = "Initializing"

	// reasonAwaitingExternalSystem is the initial reason for
	// ExternalRemediationComplete.
	reasonAwaitingExternalSystem = "AwaitingExternalSystem"
)

// ExternalRemediationRequestReconciler reconciles ExternalRemediationRequest
// objects. Per ADR-040 the reconciler is a six-branch state machine driven by
// the deletion timestamp and the two status conditions. Branches that act on
// the Node (apply path, cleanup paths) are filled in by subsequent slices —
// this scaffolding lands the dispatcher, the cleanup finalizer, and the
// initial condition writes.
type ExternalRemediationRequestReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//nolint:lll // kubebuilder RBAC markers must stay on one line
// +kubebuilder:rbac:groups=nvsentinel.nvidia.com,resources=externalremediationrequests,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nvsentinel.nvidia.com,resources=externalremediationrequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nvsentinel.nvidia.com,resources=externalremediationrequests/finalizers,verbs=update

// Reconcile drives the ERR through its lifecycle. This slice ensures the
// cleanup finalizer is present and the initial Unknown conditions are written;
// subsequent slices fill in branches 2-5 of the dispatcher.
func (r *ExternalRemediationRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err nvsentinelv1.ExternalRemediationRequest
	if e := r.Get(ctx, req.NamespacedName, &err); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}

	if r.needsInitialization(&err) {
		return r.reconcileInitialize(ctx, &err)
	}

	return r.dispatch(ctx, &err)
}

// needsInitialization returns true if either the cleanup finalizer or either
// of the two initial status conditions is absent. After initialization has run
// once, subsequent reconciles fall through to the dispatcher; conditions
// written by the external system (e.g. ExternalRemediationComplete=True) are
// never overwritten, because needsInitialization only checks for *presence*.
func (r *ExternalRemediationRequestReconciler) needsInitialization(errObj *nvsentinelv1.ExternalRemediationRequest) bool {
	if !controllerutil.ContainsFinalizer(errObj, ExternalRemediationFinalizer) {
		return true
	}

	conds := statusConditions(errObj)
	if meta.FindStatusCondition(conds, ConditionNVSentinelOwnershipReleased) == nil {
		return true
	}

	if meta.FindStatusCondition(conds, ConditionExternalRemediationComplete) == nil {
		return true
	}

	return false
}

// reconcileInitialize ensures the cleanup finalizer is present and the initial
// Unknown conditions are written. The finalizer and the status conditions are
// updated in separate API calls (different subresources); the conditions write
// only fills in absent conditions, so partial state from a previous interrupted
// init is recovered cleanly on re-reconcile.
func (r *ExternalRemediationRequestReconciler) reconcileInitialize(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(errObj, ExternalRemediationFinalizer) {
		updated := errObj.DeepCopy()
		controllerutil.AddFinalizer(updated, ExternalRemediationFinalizer)

		if err := r.Update(ctx, updated); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer to ExternalRemediationRequest %s: %w", errObj.Name, err)
		}

		slog.InfoContext(ctx, "Added cleanup finalizer to ExternalRemediationRequest", "name", errObj.Name)
		// controller-runtime's own-kind watch re-enqueues this object after
		// the metadata Update; the next reconcile will write initial conditions.
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, r.setInitialConditions(ctx, errObj)
}

// setInitialConditions writes the two initial Unknown conditions if absent.
// Existing conditions are preserved as-is — this is the path through which
// re-runs of init are idempotent and through which conditions set by external
// actors survive the init pass.
func (r *ExternalRemediationRequestReconciler) setInitialConditions(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) error {
	existing := statusConditions(errObj)
	conditions := append([]metav1.Condition(nil), existing...)
	changed := false

	if meta.FindStatusCondition(conditions, ConditionNVSentinelOwnershipReleased) == nil {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    ConditionNVSentinelOwnershipReleased,
			Status:  metav1.ConditionUnknown,
			Reason:  reasonInitializing,
			Message: "Reconciler has not yet applied the release taint and managed=false label.",
		})

		changed = true
	}

	if meta.FindStatusCondition(conditions, ConditionExternalRemediationComplete) == nil {
		meta.SetStatusCondition(&conditions, metav1.Condition{
			Type:    ConditionExternalRemediationComplete,
			Status:  metav1.ConditionUnknown,
			Reason:  reasonAwaitingExternalSystem,
			Message: "External system has not yet reported completion.",
		})

		changed = true
	}

	if !changed {
		return nil
	}

	return r.patchStatusConditions(ctx, errObj, conditions)
}

// dispatch is the six-branch state machine described in ADR-040. Branch 1
// (initialization) is handled before dispatch is called; branches 2-5 are
// stubs that subsequent slices fill in. Branch 6 catches the steady-state
// "released, awaiting external system" case where there is nothing to do.
func (r *ExternalRemediationRequestReconciler) dispatch(
	_ context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	conds := statusConditions(errObj)

	switch {
	case !errObj.DeletionTimestamp.IsZero():
		// Branch 2: deletion-driven cleanup. Filled in by the resolution-paths slice.
		return ctrl.Result{}, nil

	case isConditionStatus(conds, ConditionNVSentinelOwnershipReleased, metav1.ConditionUnknown):
		// Branch 3: apply path (release taint + managed=false). Filled in by the apply-path slice.
		return ctrl.Result{}, nil

	case meta.IsStatusConditionTrue(conds, ConditionExternalRemediationComplete):
		// Branch 4: external system signalled success — run cleanup. Filled in by the resolution-paths slice.
		return ctrl.Result{}, nil

	case meta.IsStatusConditionFalse(conds, ConditionExternalRemediationComplete):
		// Branch 5: external system signalled failure — intentional no-op (asymmetric handling per ADR-040).
		return ctrl.Result{}, nil

	default:
		// Branch 6: released and waiting on the external system. Nothing to do.
		return ctrl.Result{}, nil
	}
}

// statusConditions returns the ERR's status conditions as []metav1.Condition,
// handling the nil-status case (a freshly created ERR has no status).
func statusConditions(errObj *nvsentinelv1.ExternalRemediationRequest) []metav1.Condition {
	if errObj.Status == nil {
		return nil
	}

	return condition.ToMetav1Slice(errObj.Status.Conditions)
}

// isConditionStatus returns true if the named condition exists and has the
// given status. Equivalent to meta.IsStatusConditionPresentAndEqual but
// expressed in terms of the helpers already used by the rest of this file.
func isConditionStatus(conds []metav1.Condition, condType string, status metav1.ConditionStatus) bool {
	c := meta.FindStatusCondition(conds, condType)
	return c != nil && c.Status == status
}

// patchStatusConditions writes the given conditions back into the ERR's
// status via a status subresource merge patch. Re-fetches the object first to
// minimise the conflict window with concurrent writers (e.g. external systems
// mutating ExternalRemediationComplete).
func (r *ExternalRemediationRequestReconciler) patchStatusConditions(
	ctx context.Context,
	errObj *nvsentinelv1.ExternalRemediationRequest,
	conditions []metav1.Condition,
) error {
	var latest nvsentinelv1.ExternalRemediationRequest
	if err := r.Get(ctx, client.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}, &latest); err != nil {
		return fmt.Errorf("refreshing ExternalRemediationRequest %s before status patch: %w", errObj.Name, err)
	}

	updated := latest.DeepCopy()
	if updated.Status == nil {
		updated.Status = &protos.ExternalRemediationRequestStatus{}
	}

	updated.Status.Conditions = condition.FromMetav1Slice(conditions)

	if reflect.DeepEqual(latest.Status, updated.Status) {
		return nil
	}

	if err := r.Status().Patch(ctx, updated, client.MergeFrom(&latest)); err != nil {
		return fmt.Errorf("patching ExternalRemediationRequest %s status: %w", errObj.Name, err)
	}

	slog.InfoContext(ctx, "ExternalRemediationRequest status conditions updated", "name", errObj.Name)

	return nil
}

// SetupWithManager registers the reconciler with the controller-runtime
// manager. Watches the primary ERR kind and Nodes (Nodes map to ERRs by
// spec.healthEvent.nodeName so future slices can react to taint drift on the
// released node; this slice wires the watch but takes no action).
func (r *ExternalRemediationRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nvsentinelv1.ExternalRemediationRequest{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.mapNodeToERRs),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("externalremediationrequest").
		Complete(r)
}

// mapNodeToERRs returns the ERRs whose spec.healthEvent.nodeName matches the
// given Node. Used by future slices that need to react to Node changes (taint
// drift detection, label drift detection) — this slice wires the mapping but
// takes no action on the resulting reconcile.
func (r *ExternalRemediationRequestReconciler) mapNodeToERRs(ctx context.Context, obj client.Object) []ctrl.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}

	var errs nvsentinelv1.ExternalRemediationRequestList
	if err := r.List(ctx, &errs); err != nil {
		slog.ErrorContext(ctx, "listing ExternalRemediationRequests for Node mapping",
			"node", node.Name, "error", err)

		return nil
	}

	var requests []ctrl.Request

	for i := range errs.Items {
		e := &errs.Items[i]
		if e.Spec != nil && e.Spec.HealthEvent != nil && e.Spec.HealthEvent.NodeName == node.Name {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{Name: e.Name, Namespace: e.Namespace},
			})
		}
	}

	return requests
}
