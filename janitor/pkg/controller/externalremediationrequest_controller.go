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
	"time"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/nvidia/nvsentinel/commons/pkg/managed"
	"github.com/nvidia/nvsentinel/commons/pkg/tracing"
	"github.com/nvidia/nvsentinel/data-models/pkg/model"
	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	nvsentinelv1 "github.com/nvidia/nvsentinel/janitor/api/v1alpha1"
	"github.com/nvidia/nvsentinel/janitor/pkg/condition"
	"github.com/nvidia/nvsentinel/janitor/pkg/metrics"
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

	// ReleaseTaintKey is the key of the taint the reconciler applies to a Node
	// to release it from NVSentinel ownership. The taint's value carries the
	// owning ERR's metadata.name so operators can discover which ERR holds the
	// node via `kubectl describe node` without consulting separate annotations.
	// Per ADR-040.
	ReleaseTaintKey = "nvsentinel.nvidia.com/external-remediation"

	// ReasonReleaseTaintApplied is the NVSentinelOwnershipReleased=True
	// reason set after the release taint and managed=false label land.
	ReasonReleaseTaintApplied = "ReleaseTaintApplied"

	// ReasonReleaseTaintFailed is the NVSentinelOwnershipReleased=False
	// reason set when the apply path cannot complete — RBAC forbidden, taint
	// drift (a taint with our key but a different value), or a missing
	// healthEvent.nodeName.
	ReasonReleaseTaintFailed = "ReleaseTaintFailed"

	// Kubernetes event reasons emitted on the ERR object. These show up in
	// `kubectl describe err <name>` so operators can audit the lifecycle
	// without consulting the controller logs.
	eventReasonReleaseTaintApplied   = "ReleaseTaintApplied"
	eventReasonReleaseTaintFailed    = "ReleaseTaintFailed"
	eventReasonReleaseTaintRemoved   = "ReleaseTaintRemoved"
	eventReasonOperatorDeleteRequest = "OperatorDeleteRequested"

	// Event-message reason qualifiers for ReleaseTaintRemoved so the same
	// reason can disambiguate which cleanup path closed the ERR.
	closeReasonExternalRemediationCompleteTrue = "ExternalRemediationCompleteTrue"
	closeReasonOperatorInitiated               = "OperatorInitiated"
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
	// Recorder emits Kubernetes events against the ERR object. SetupWithManager
	// populates it from mgr.GetEventRecorderFor; tests may inject a fake via
	// record.NewFakeRecorder.
	Recorder record.EventRecorder
}

// recommendedActionLabel produces a stable Prometheus label value identifying
// the action that triggered this ERR. Falls back to "unknown" if the spec
// hasn't been populated yet — this should not happen post-admission but the
// metric path tolerates it rather than panicking.
func recommendedActionLabel(errObj *nvsentinelv1.ExternalRemediationRequest) string {
	if errObj.Spec == nil || errObj.Spec.HealthEvent == nil {
		return "unknown"
	}

	if name := model.GetEffectiveActionName(errObj.Spec.HealthEvent); name != "" {
		return name
	}

	return "unknown"
}

// errNodeLabel returns the node-name label value for ERR metrics, defaulting
// to "unknown" when the spec is incomplete (matches recommendedActionLabel).
func errNodeLabel(errObj *nvsentinelv1.ExternalRemediationRequest) string {
	if errObj.Spec == nil || errObj.Spec.HealthEvent == nil || errObj.Spec.HealthEvent.NodeName == "" {
		return "unknown"
	}

	return errObj.Spec.HealthEvent.NodeName
}

// emitEvent records a Kubernetes event against the ERR. Tolerates a nil
// recorder (test paths that construct the reconciler manually) so the
// observability slice doesn't break those flows.
func (r *ExternalRemediationRequestReconciler) emitEvent(
	errObj *nvsentinelv1.ExternalRemediationRequest, eventType, reason, message string,
) {
	if r.Recorder == nil {
		return
	}

	r.Recorder.Event(errObj, eventType, reason, message)
}

//nolint:lll // kubebuilder RBAC markers must stay on one line
// +kubebuilder:rbac:groups=nvsentinel.nvidia.com,resources=externalremediationrequests,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=nvsentinel.nvidia.com,resources=externalremediationrequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nvsentinel.nvidia.com,resources=externalremediationrequests/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch;patch

// Reconcile drives the ERR through its lifecycle. Each reconcile is wrapped
// in an OTEL span linked to the originating health-monitor's trace via the
// trace-id / span-id annotations that fault-remediation propagates onto the
// ERR template — so a single trace covers health event -> quarantine ->
// drain -> fault-remediation -> ERR creation -> ERR close.
func (r *ExternalRemediationRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var err nvsentinelv1.ExternalRemediationRequest
	if e := r.Get(ctx, req.NamespacedName, &err); e != nil {
		return ctrl.Result{}, client.IgnoreNotFound(e)
	}

	annotations := err.GetAnnotations()
	ctx, span := tracing.StartSpanWithLinkFromTraceContext(
		ctx,
		annotations[tracing.TraceIDAnnotationKey],
		annotations[tracing.SpanIDAnnotationKey],
		"janitor.externalremediationrequest.reconcile",
	)
	defer span.End()

	span.SetAttributes(
		attribute.String("err.name", err.Name),
		attribute.String("err.namespace", err.Namespace),
		attribute.String("err.node", errNodeLabel(&err)),
		attribute.String("err.recommended_action", recommendedActionLabel(&err)),
	)

	if r.needsInitialization(&err) {
		span.SetAttributes(attribute.String("err.branch", "init"))
		return r.reconcileInitialize(ctx, &err)
	}

	result, dispatchErr := r.dispatch(ctx, &err)
	if dispatchErr != nil {
		tracing.RecordError(span, dispatchErr)
	}

	return result, dispatchErr
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
// init is recovered cleanly on re-reconcile. Emits an err_total{phase=created}
// counter the first time the initial conditions actually land.
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

	changed, err := r.setInitialConditions(ctx, errObj)
	if err != nil {
		return ctrl.Result{}, err
	}

	if changed {
		// First time the initial conditions actually landed for this ERR —
		// count exactly once even if reconcileInitialize is invoked again
		// (idempotent setInitialConditions short-circuits on re-entry).
		metrics.GlobalMetrics.IncERRTotal(metrics.ERRPhaseCreated, "")
	}

	return ctrl.Result{}, nil
}

// setInitialConditions writes the two initial Unknown conditions if absent.
// Existing conditions are preserved as-is — this is the path through which
// re-runs of init are idempotent and through which conditions set by external
// actors survive the init pass. Returns (true, nil) when conditions were
// actually written; (false, nil) is a no-op for an already-initialised ERR.
func (r *ExternalRemediationRequestReconciler) setInitialConditions(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (bool, error) {
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
		return false, nil
	}

	patched, err := r.patchStatusConditions(ctx, errObj, conditions)
	return patched, err
}

// dispatch is the six-branch state machine described in ADR-040. Branch 1
// (initialization) is handled before dispatch is called; branches 2, 4, and 5
// remain stubs filled in by subsequent slices. Branch 3 (apply path) is
// implemented in reconcileApply. Branch 6 catches the steady-state "released,
// awaiting external system" case where there is nothing to do.
func (r *ExternalRemediationRequestReconciler) dispatch(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	conds := statusConditions(errObj)

	switch {
	case !errObj.DeletionTimestamp.IsZero():
		// Branch 2: deletion-driven cleanup — remove taint+label, then remove the finalizer.
		return r.reconcileCleanupOnDeletion(ctx, errObj)

	case isConditionStatus(conds, ConditionNVSentinelOwnershipReleased, metav1.ConditionUnknown):
		// Branch 3: apply path — release taint + managed=false label, single PATCH.
		return r.reconcileApply(ctx, errObj)

	case meta.IsStatusConditionTrue(conds, ConditionExternalRemediationComplete):
		// Branch 4: external system signalled success — remove taint+label; ERR stays as historical record.
		return r.reconcileCleanupAfterComplete(ctx, errObj)

	case meta.IsStatusConditionFalse(conds, ConditionExternalRemediationComplete):
		// Branch 5: external system signalled failure — asymmetric no-op per ADR-040.
		return r.reconcileNoOpOnFalse(ctx, errObj)

	default:
		// Branch 6: released and waiting on the external system. Nothing to do.
		return ctrl.Result{}, nil
	}
}

// nodeMissingRequeue is how long to wait before re-checking a Node that
// doesn't yet exist on the apiserver. The Node may show up shortly (cluster
// autoscaler, kubelet registration) so we don't immediately fail the ERR.
const nodeMissingRequeue = 30 * time.Second

// reconcileApply implements branch 3: drive a fresh ERR (NVSentinelOwnershipReleased=Unknown)
// to the released state by applying the release taint and managed=false label in a single
// strategic-merge PATCH on the target Node, then transitioning the condition to True.
//
// Failure modes per ADR-040:
//
//   - Empty spec.healthEvent.nodeName — admission webhook should catch this, but if it slips
//     through, transition to False (persistent; not a controller-side problem to retry).
//   - Node not found — transient. Leave the condition Unknown and requeue; the Node may show
//     up shortly via cluster autoscaler or kubelet registration.
//   - Existing taint with this ERR's name as value — already-applied. Skip the PATCH and
//     transition to True; this handles the case where a prior reconcile patched the node but
//     failed to update the condition (e.g. the controller crashed between PATCH and Status().Patch).
//   - Existing taint with a different value — drift. Some other ERR owns the node. Transition
//     to False; the operator must `kubectl delete err <other-name>` to release ownership.
//   - Forbidden — persistent RBAC denial. Transition to False so the operator sees the failure;
//     controller-runtime backoff cannot fix RBAC.
//   - Any other apiserver error — transient. Return the error so controller-runtime backs off.
func (r *ExternalRemediationRequestReconciler) reconcileApply(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	nodeName := ""
	if errObj.Spec != nil && errObj.Spec.HealthEvent != nil {
		nodeName = errObj.Spec.HealthEvent.NodeName
	}

	if nodeName == "" {
		msg := "ExternalRemediationRequest.spec.healthEvent.nodeName is empty; cannot apply release taint"
		return ctrl.Result{}, r.transitionToReleaseFailure(ctx, errObj, msg)
	}

	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			slog.WarnContext(ctx, "target node not found; requeueing",
				"err", errObj.Name, "node", nodeName, "requeueAfter", nodeMissingRequeue)

			return ctrl.Result{RequeueAfter: nodeMissingRequeue}, nil
		}

		return ctrl.Result{}, fmt.Errorf("get node %q for ERR %q: %w", nodeName, errObj.Name, err)
	}

	if existing := findTaintByKey(node.Spec.Taints, ReleaseTaintKey); existing != nil {
		if existing.Value != errObj.Name {
			msg := fmt.Sprintf(
				"node %q already tainted by ExternalRemediationRequest %q; another ERR owns this node",
				nodeName, existing.Value)
			slog.WarnContext(ctx, "release taint drift detected",
				"err", errObj.Name, "node", nodeName, "existing_owner", existing.Value)

			return ctrl.Result{}, r.transitionToReleaseFailure(ctx, errObj, msg)
		}
		// Taint already in place with our name — verify the label is also present,
		// then transition the condition without issuing a redundant PATCH.
		if node.Labels[managed.ManagedLabelKey] == managed.ManagedLabelValueFalse {
			slog.InfoContext(ctx, "release taint and managed=false label already in place; transitioning condition",
				"err", errObj.Name, "node", nodeName)

			msg := fmt.Sprintf("release taint %s=%s and managed=false label already present on node %q",
				ReleaseTaintKey, errObj.Name, nodeName)
			return ctrl.Result{}, r.transitionToReleaseSuccess(ctx, errObj, msg)
		}
		// Taint is right but the label is missing — patch only the label below.
	}

	nodeToUpdate := node.DeepCopy()

	if findTaintByKey(nodeToUpdate.Spec.Taints, ReleaseTaintKey) == nil {
		nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
			Key:    ReleaseTaintKey,
			Value:  errObj.Name,
			Effect: corev1.TaintEffectNoSchedule,
		})
	}

	if nodeToUpdate.Labels == nil {
		nodeToUpdate.Labels = map[string]string{}
	}

	nodeToUpdate.Labels[managed.ManagedLabelKey] = managed.ManagedLabelValueFalse

	if err := r.Patch(ctx, nodeToUpdate, client.StrategicMergeFrom(&node)); err != nil {
		if apierrors.IsForbidden(err) {
			msg := fmt.Sprintf("forbidden to patch node %q: %v", nodeName, err)
			slog.ErrorContext(ctx, "release taint apply forbidden by RBAC", "err", errObj.Name, "node", nodeName, "error", err)

			return ctrl.Result{}, r.transitionToReleaseFailure(ctx, errObj, msg)
		}

		return ctrl.Result{}, fmt.Errorf("patch node %q with release taint + managed=false: %w", nodeName, err)
	}

	slog.InfoContext(ctx, "applied release taint and managed=false label to node",
		"err", errObj.Name, "node", nodeName)

	msg := fmt.Sprintf("applied release taint %s=%s and managed=false label to node %q",
		ReleaseTaintKey, errObj.Name, nodeName)
	return ctrl.Result{}, r.transitionToReleaseSuccess(ctx, errObj, msg)
}

// transitionToReleaseSuccess marks the apply path complete. Fires the
// err_total{released, success} counter, the err_open{awaiting} gauge, and
// the ReleaseTaintApplied Kubernetes event exactly once per transition by
// gating on whether the status patch actually mutated state.
func (r *ExternalRemediationRequestReconciler) transitionToReleaseSuccess(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest, message string,
) error {
	changed, err := r.transitionReleased(ctx, errObj, metav1.ConditionTrue, ReasonReleaseTaintApplied, message)
	if err != nil {
		return err
	}

	if !changed {
		return nil
	}

	metrics.GlobalMetrics.IncERRTotal(metrics.ERRPhaseReleased, metrics.ERRResultSuccess)
	metrics.GlobalMetrics.AdjustERROpen(errNodeLabel(errObj), recommendedActionLabel(errObj),
		metrics.ERROpenStateAwaiting, 1)
	r.emitEvent(errObj, corev1.EventTypeNormal, eventReasonReleaseTaintApplied, message)

	return nil
}

// transitionToReleaseFailure marks the apply path persistently failed. Drift
// case (foreign taint owner), forbidden, and missing-nodeName all funnel
// through here. Fires the err_total{released, failure} counter and the
// ReleaseTaintFailed Kubernetes event exactly once per transition. Does NOT
// touch the err_open gauge — the ERR sits in a terminal-failure state, not
// an in-flight one, so operators can scrape err_total{phase=released,result=failure}
// for the counter and observe the absence from err_open{state=awaiting}.
func (r *ExternalRemediationRequestReconciler) transitionToReleaseFailure(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest, message string,
) error {
	changed, err := r.transitionReleased(ctx, errObj, metav1.ConditionFalse, ReasonReleaseTaintFailed, message)
	if err != nil {
		return err
	}

	if !changed {
		return nil
	}

	metrics.GlobalMetrics.IncERRTotal(metrics.ERRPhaseReleased, metrics.ERRResultFailure)
	r.emitEvent(errObj, corev1.EventTypeWarning, eventReasonReleaseTaintFailed, message)

	return nil
}

// reconcileCleanupAfterComplete implements branch 4. The external system has
// reported success, so remove the release taint and managed=false label from
// the target Node. The ERR stays in the cluster with its finalizer attached as
// a historical record; operators can clean up historical ERRs en masse via
// `kubectl delete err --all` or rely on the TTL reconciler.
//
// ExternalRemediationComplete=True is already terminal — no condition
// transition is performed by this branch. Observability fires only on the
// reconcile pass that actually performs the cleanup PATCH (subsequent
// re-reconciles short-circuit via reconcileCleanup's idempotency).
func (r *ExternalRemediationRequestReconciler) reconcileCleanupAfterComplete(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	changed, err := r.reconcileCleanup(ctx, errObj)
	if err != nil {
		return ctrl.Result{}, err
	}

	if changed {
		r.recordClose(errObj, metrics.ERRResultSuccess, closeReasonExternalRemediationCompleteTrue)
		// external_response{success} is co-emitted with closed{success} because
		// the True observation and our cleanup PATCH happen on the same reconcile
		// pass for the first time only.
		metrics.GlobalMetrics.IncERRTotal(metrics.ERRPhaseExternalResponse, metrics.ERRResultSuccess)
	}

	return ctrl.Result{}, nil
}

// reconcileCleanupOnDeletion implements branch 2. An operator (or external
// automation) has run `kubectl delete err <name>`; the finalizer keeps the
// object alive until we run cleanup. Apply the same cleanup PATCH as branch 4,
// then remove the finalizer so Kubernetes garbage-collects the ERR.
//
// Idempotent against post-True state — if branch 4 already ran cleanup, the
// reconcileCleanup helper short-circuits and we proceed straight to finalizer
// removal. The closed{result=operator_deleted} counter only fires when we
// were the path that actually closed the ERR (i.e. cleanup PATCH ran here,
// not earlier via branch 4).
func (r *ExternalRemediationRequestReconciler) reconcileCleanupOnDeletion(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(errObj, ExternalRemediationFinalizer) {
		// Finalizer already gone — nothing left for us to do.
		return ctrl.Result{}, nil
	}

	r.emitEvent(errObj, corev1.EventTypeNormal, eventReasonOperatorDeleteRequest,
		"deletion requested; running cleanup before releasing finalizer")

	changed, err := r.reconcileCleanup(ctx, errObj)
	if err != nil {
		return ctrl.Result{}, err
	}

	if changed {
		r.recordClose(errObj, metrics.ERRResultOperatorDeleted, closeReasonOperatorInitiated)
	}

	updated := errObj.DeepCopy()
	controllerutil.RemoveFinalizer(updated, ExternalRemediationFinalizer)

	if err := r.Update(ctx, updated); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing cleanup finalizer from ExternalRemediationRequest %s: %w",
			errObj.Name, err)
	}

	slog.InfoContext(ctx, "removed cleanup finalizer; ExternalRemediationRequest will be garbage-collected",
		"err", errObj.Name)

	return ctrl.Result{}, nil
}

// recordClose emits the metrics + event triple that fires on every ERR close.
// result is one of metrics.ERRResult{Success,OperatorDeleted}; closeReason
// disambiguates which path closed it in the human-readable event message.
func (r *ExternalRemediationRequestReconciler) recordClose(
	errObj *nvsentinelv1.ExternalRemediationRequest, result, closeReason string,
) {
	node := errNodeLabel(errObj)
	action := recommendedActionLabel(errObj)

	metrics.GlobalMetrics.IncERRTotal(metrics.ERRPhaseClosed, result)
	metrics.GlobalMetrics.AdjustERROpen(node, action, metrics.ERROpenStateAwaiting, -1)

	if !errObj.CreationTimestamp.IsZero() {
		metrics.GlobalMetrics.ObserveERRAge(action, result,
			time.Since(errObj.CreationTimestamp.Time).Seconds())
	}

	r.emitEvent(errObj, corev1.EventTypeNormal, eventReasonReleaseTaintRemoved,
		fmt.Sprintf("release taint and managed=false label removed (%s)", closeReason))
}

// reconcileCleanup is the shared cleanup PATCH: removes the release taint
// (only if its value matches this ERR's metadata.name — drift-safe) and
// removes the managed label entirely. Both mutations land in one
// strategic-merge PATCH against the Node. Returns (true, nil) when the PATCH
// actually mutated the Node; (false, nil) when there was nothing to clean up
// (target Node missing, taint already absent, label already absent, or all
// of the above). Counter callers use this signal to count exactly once per
// real close.
//
// Per ADR-040, removing the label is preferred over setting it to "true" —
// absence is the default-managed state and leaves no rotting hint behind.
//
// Short-circuits when there's nothing to remove so re-reconciles in either
// cleanup branch do not generate spurious PATCHes. The Node also vanishing
// (e.g. terminated by an external system) is treated as already-clean.
func (r *ExternalRemediationRequestReconciler) reconcileCleanup(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (bool, error) {
	nodeName := ""
	if errObj.Spec != nil && errObj.Spec.HealthEvent != nil {
		nodeName = errObj.Spec.HealthEvent.NodeName
	}

	if nodeName == "" {
		// Nothing to clean if we never knew which Node to release in the first place.
		return false, nil
	}

	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			slog.InfoContext(ctx, "target Node already gone; nothing to clean up",
				"err", errObj.Name, "node", nodeName)

			return false, nil
		}

		return false, fmt.Errorf("get node %q for ERR %q cleanup: %w", nodeName, errObj.Name, err)
	}

	nodeToUpdate := node.DeepCopy()
	changed := false

	if existing := findTaintByKey(nodeToUpdate.Spec.Taints, ReleaseTaintKey); existing != nil {
		switch existing.Value {
		case errObj.Name:
			nodeToUpdate.Spec.Taints = removeTaintByKey(nodeToUpdate.Spec.Taints, ReleaseTaintKey)
			changed = true
		default:
			// Drift: another ERR claims the taint. Leave it alone — that ERR's
			// own cleanup path will remove it. Logging only; not an error.
			slog.WarnContext(ctx, "release taint owned by a different ERR; leaving in place during cleanup",
				"err", errObj.Name, "node", nodeName, "existing_owner", existing.Value)
		}
	}

	if _, ok := nodeToUpdate.Labels[managed.ManagedLabelKey]; ok {
		delete(nodeToUpdate.Labels, managed.ManagedLabelKey)
		changed = true
	}

	if !changed {
		return false, nil
	}

	if err := r.Patch(ctx, nodeToUpdate, client.StrategicMergeFrom(&node)); err != nil {
		return false, fmt.Errorf("patch node %q for ERR %q cleanup: %w", nodeName, errObj.Name, err)
	}

	slog.InfoContext(ctx, "removed release taint and managed label from node",
		"err", errObj.Name, "node", nodeName)

	return true, nil
}

// reconcileNoOpOnFalse implements branch 5. The external system has reported
// failure via ExternalRemediationComplete=False — this is intentionally
// asymmetric with True. Per ADR-040: when the external system signals failure,
// NVSentinel has no knowledge of what state the node was left in (mid-RMA,
// partial repair, hardware swapped but not validated, ...), so returning the
// node to user workloads on that signal would be unsafe. The release taint
// and managed=false label STAY; the node remains released until either:
//
//   - the external system patches ExternalRemediationComplete=True later
//     (which fires branch 4 and runs cleanup), or
//   - an operator runs `kubectl delete err <name>` (which fires branch 2 and
//     runs cleanup + finalizer remove).
//
// This function deliberately does NOT call reconcileCleanup or any other Node
// mutation — the explicit absence is the design contract.
func (r *ExternalRemediationRequestReconciler) reconcileNoOpOnFalse(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
) (ctrl.Result, error) {
	complete := meta.FindStatusCondition(statusConditions(errObj), ConditionExternalRemediationComplete)

	nodeName := ""
	if errObj.Spec != nil && errObj.Spec.HealthEvent != nil {
		nodeName = errObj.Spec.HealthEvent.NodeName
	}

	var reason, message string
	if complete != nil {
		reason = complete.Reason
		message = complete.Message
	}

	slog.InfoContext(ctx,
		"external system reported failure; node remains released until operator deletes ERR or external system retries",
		"err", errObj.Name,
		"node", nodeName,
		"external_reason", reason,
		"external_message", message,
	)

	return ctrl.Result{}, nil
}

// removeTaintByKey returns a new slice with all taints whose key matches
// removed. Allocates a fresh backing array so callers don't accidentally
// mutate the original.
func removeTaintByKey(taints []corev1.Taint, key string) []corev1.Taint {
	out := make([]corev1.Taint, 0, len(taints))

	for i := range taints {
		if taints[i].Key != key {
			out = append(out, taints[i])
		}
	}

	return out
}

// findTaintByKey returns a pointer to the first taint with the given key, or
// nil. Returns a pointer into the input slice — callers must not mutate the
// returned taint in place if the slice will be patched later.
func findTaintByKey(taints []corev1.Taint, key string) *corev1.Taint {
	for i := range taints {
		if taints[i].Key == key {
			return &taints[i]
		}
	}

	return nil
}

// transitionReleased sets NVSentinelOwnershipReleased to the given status with
// the given reason / message via a status subresource merge patch. Preserves
// any existing ExternalRemediationComplete condition (set by the external
// system) untouched. Returns (true, nil) when the patch actually mutated the
// status; (false, nil) when the condition was already in the requested state
// (idempotent re-entry).
func (r *ExternalRemediationRequestReconciler) transitionReleased(
	ctx context.Context, errObj *nvsentinelv1.ExternalRemediationRequest,
	status metav1.ConditionStatus, reason, message string,
) (bool, error) {
	existing := statusConditions(errObj)
	conditions := append([]metav1.Condition(nil), existing...)

	meta.SetStatusCondition(&conditions, metav1.Condition{
		Type:    ConditionNVSentinelOwnershipReleased,
		Status:  status,
		Reason:  reason,
		Message: message,
	})

	return r.patchStatusConditions(ctx, errObj, conditions)
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
// mutating ExternalRemediationComplete). Returns (true, nil) when the patch
// actually mutated state; (false, nil) when the in-memory conditions already
// matched what's on the apiserver.
func (r *ExternalRemediationRequestReconciler) patchStatusConditions(
	ctx context.Context,
	errObj *nvsentinelv1.ExternalRemediationRequest,
	conditions []metav1.Condition,
) (bool, error) {
	var latest nvsentinelv1.ExternalRemediationRequest
	if err := r.Get(ctx, client.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}, &latest); err != nil {
		return false, fmt.Errorf("refreshing ExternalRemediationRequest %s before status patch: %w", errObj.Name, err)
	}

	updated := latest.DeepCopy()
	if updated.Status == nil {
		updated.Status = &protos.ExternalRemediationRequestStatus{}
	}

	updated.Status.Conditions = condition.FromMetav1Slice(conditions)

	if reflect.DeepEqual(latest.Status, updated.Status) {
		return false, nil
	}

	if err := r.Status().Patch(ctx, updated, client.MergeFrom(&latest)); err != nil {
		return false, fmt.Errorf("patching ExternalRemediationRequest %s status: %w", errObj.Name, err)
	}

	slog.InfoContext(ctx, "ExternalRemediationRequest status conditions updated", "name", errObj.Name)

	return true, nil
}

// SetupWithManager registers the reconciler with the controller-runtime
// manager. Watches the primary ERR kind and Nodes (Nodes map to ERRs by
// spec.healthEvent.nodeName so future slices can react to taint drift on the
// released node). Also wires the event recorder so the reconciler can attach
// Kubernetes events to the ERR object (visible via `kubectl describe err`).
func (r *ExternalRemediationRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("externalremediationrequest-controller")
	}

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
