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
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	nvsentinelv1 "github.com/nvidia/nvsentinel/janitor/api/v1alpha1"
)

const testERRNamespace = "default"

// newERRReconciler returns a reconciler bound to the envtest API server.
// Must only be called from within Ginkgo blocks (BeforeSuite populates cfg).
func newERRReconciler() *ExternalRemediationRequestReconciler {
	c, err := ctrlclient.New(cfg, ctrlclient.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	return &ExternalRemediationRequestReconciler{
		Client: c,
		Scheme: scheme.Scheme,
	}
}

// newTestERR returns a minimal ExternalRemediationRequest object.
func newTestERR(name, nodeName string) *nvsentinelv1.ExternalRemediationRequest {
	return &nvsentinelv1.ExternalRemediationRequest{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "nvsentinel.nvidia.com/v1",
			Kind:       "ExternalRemediationRequest",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testERRNamespace,
		},
		Spec: &protos.ExternalRemediationRequestSpec{
			HealthEvent: &protos.HealthEvent{
				Id:                "he-" + name,
				NodeName:          nodeName,
				IsFatal:           true,
				RecommendedAction: protos.RecommendedAction_CUSTOM,
				Message:           "synthetic test fault",
			},
		},
	}
}

// reconcileToSteadyState drives Reconcile repeatedly so the multi-pass init
// (finalizer Update, then status Patch) completes without the test caring
// about exact pass counts.
func reconcileToSteadyState(
	ctx context.Context,
	r *ExternalRemediationRequestReconciler,
	key ctrlclient.ObjectKey,
	maxPasses int,
) *nvsentinelv1.ExternalRemediationRequest {
	GinkgoHelper()

	for i := 0; i < maxPasses; i++ {
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred(), "reconcile pass %d", i+1)
	}

	var out nvsentinelv1.ExternalRemediationRequest
	Expect(r.Client.Get(ctx, key, &out)).To(Succeed())

	return &out
}

var _ = Describe("ExternalRemediationRequest Controller", func() {
	var (
		ctx context.Context
		r   *ExternalRemediationRequestReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		r = newERRReconciler()
	})

	It("adds the cleanup finalizer and initial Unknown conditions on a fresh ERR", func() {
		errObj := newTestERR("fresh-err-1", "node-fresh-1")
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)

		Expect(controllerutil.ContainsFinalizer(got, ExternalRemediationFinalizer)).
			To(BeTrue(), "cleanup finalizer must be added")

		Expect(got.Status).NotTo(BeNil(), "Status must be populated")
		Expect(got.Status.Conditions).To(HaveLen(2), "two initial conditions expected")

		released := findERRCondition(got, ConditionNVSentinelOwnershipReleased)
		Expect(released).NotTo(BeNil())
		Expect(released.Status).To(Equal("Unknown"))
		Expect(released.Reason).To(Equal(reasonInitializing))
		Expect(released.Message).NotTo(BeEmpty())
		Expect(released.LastTransitionTime).NotTo(BeNil())

		complete := findERRCondition(got, ConditionExternalRemediationComplete)
		Expect(complete).NotTo(BeNil())
		Expect(complete.Status).To(Equal("Unknown"))
		Expect(complete.Reason).To(Equal(reasonAwaitingExternalSystem))
		Expect(complete.Message).NotTo(BeEmpty())
		Expect(complete.LastTransitionTime).NotTo(BeNil())
	})

	It("is idempotent on re-reconcile (no LastTransitionTime flap)", func() {
		errObj := newTestERR("idempotent-err-1", "node-idem-1")
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)
		Expect(got.Status.Conditions).To(HaveLen(2))

		beforeTimes := map[string]time.Time{}
		for _, c := range got.Status.Conditions {
			Expect(c.LastTransitionTime).NotTo(BeNil(), "%s must have a LastTransitionTime", c.Type)
			beforeTimes[c.Type] = c.LastTransitionTime.AsTime()
		}
		Expect(beforeTimes).To(HaveLen(2))

		// Sleep so any flap would produce a strictly later timestamp.
		time.Sleep(50 * time.Millisecond)

		got = reconcileToSteadyState(ctx, r, key, 3)
		Expect(got.Status.Conditions).To(HaveLen(2))

		for _, c := range got.Status.Conditions {
			Expect(c.LastTransitionTime).NotTo(BeNil())

			before, ok := beforeTimes[c.Type]
			Expect(ok).To(BeTrue(), "unexpected condition after re-reconcile: %s", c.Type)
			Expect(c.LastTransitionTime.AsTime()).To(Equal(before),
				"%s LastTransitionTime flapped", c.Type)
		}
	})

	It("is a no-op when the ERR has a deletionTimestamp and the cleanup finalizer", func() {
		errObj := newTestERR("delete-err-1", "node-del-1")
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(forceFinalizerRemoval, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		reconcileToSteadyState(ctx, r, key, 3)

		// Delete; finalizer holds the object alive with a deletionTimestamp.
		Expect(r.Client.Delete(ctx, errObj)).To(Succeed())

		var afterDelete nvsentinelv1.ExternalRemediationRequest
		Expect(r.Client.Get(ctx, key, &afterDelete)).To(Succeed())
		Expect(afterDelete.DeletionTimestamp.IsZero()).To(BeFalse(),
			"deletionTimestamp must be set with finalizer still attached")
		Expect(controllerutil.ContainsFinalizer(&afterDelete, ExternalRemediationFinalizer)).To(BeTrue())

		condBefore := snapshotConditions(&afterDelete)
		finBefore := append([]string(nil), afterDelete.Finalizers...)

		result, recErr := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(recErr).NotTo(HaveOccurred(), "deletion-pending reconcile must not error")
		Expect(result.RequeueAfter).To(BeZero())
		Expect(result.Requeue).To(BeFalse())

		var afterReconcile nvsentinelv1.ExternalRemediationRequest
		Expect(r.Client.Get(ctx, key, &afterReconcile)).To(Succeed())

		Expect(snapshotConditions(&afterReconcile)).To(Equal(condBefore),
			"deletion-pending branch must not mutate status conditions in this slice")
		Expect(afterReconcile.Finalizers).To(Equal(finBefore),
			"deletion-pending branch must not remove the finalizer in this slice")
	})

	It("swallows reconciles for missing objects", func() {
		result, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "missing-err", Namespace: testERRNamespace},
		})
		Expect(err).NotTo(HaveOccurred(), "missing object must be swallowed via client.IgnoreNotFound")
		Expect(result.RequeueAfter).To(BeZero())
		Expect(result.Requeue).To(BeFalse())
	})
})

var _ = Describe("ExternalRemediationRequest Controller apply path (branch 3)", func() {
	var (
		ctx context.Context
		r   *ExternalRemediationRequestReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		r = newERRReconciler()
	})

	It("applies the release taint and managed=false label, transitions condition to True", func() {
		nodeName := "node-apply-1"
		Expect(r.Client.Create(ctx, newTestNode(nodeName, nil, nil))).To(Succeed())
		DeferCleanup(deleteNodeForCleanup, ctx, r, nodeName)

		errObj := newTestERR("apply-err-1", nodeName)
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)

		released := findERRCondition(got, ConditionNVSentinelOwnershipReleased)
		Expect(released).NotTo(BeNil())
		Expect(released.Status).To(Equal("True"), "NVSentinelOwnershipReleased must transition to True on successful apply")
		Expect(released.Reason).To(Equal(ReasonReleaseTaintApplied))
		Expect(released.Message).To(ContainSubstring(ReleaseTaintKey))
		Expect(released.Message).To(ContainSubstring(errObj.Name))

		var node corev1.Node
		Expect(r.Client.Get(ctx, ctrlclient.ObjectKey{Name: nodeName}, &node)).To(Succeed())

		taint := findTaintByKey(node.Spec.Taints, ReleaseTaintKey)
		Expect(taint).NotTo(BeNil(), "release taint must be applied")
		Expect(taint.Value).To(Equal(errObj.Name), "taint value must carry owning ERR's name")
		Expect(taint.Effect).To(Equal(corev1.TaintEffectNoSchedule))
		Expect(node.Labels).To(HaveKeyWithValue(ManagedLabelKey, ManagedLabelValueFalse),
			"managed=false label must be set")
	})

	It("does not re-PATCH the Node on subsequent reconciles after a successful apply", func() {
		nodeName := "node-stable-1"
		Expect(r.Client.Create(ctx, newTestNode(nodeName, nil, nil))).To(Succeed())
		DeferCleanup(deleteNodeForCleanup, ctx, r, nodeName)

		errObj := newTestERR("stable-err-1", nodeName)
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		reconcileToSteadyState(ctx, r, key, 3)

		var nodeAfterApply corev1.Node
		Expect(r.Client.Get(ctx, ctrlclient.ObjectKey{Name: nodeName}, &nodeAfterApply)).To(Succeed())
		rvAfterApply := nodeAfterApply.ResourceVersion

		// Reconcile several more times. With Released=True the dispatcher falls through to
		// branch 6 (no-op), so the Node's ResourceVersion must not advance.
		for i := 0; i < 3; i++ {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}

		var nodeAfterRereconcile corev1.Node
		Expect(r.Client.Get(ctx, ctrlclient.ObjectKey{Name: nodeName}, &nodeAfterRereconcile)).To(Succeed())
		Expect(nodeAfterRereconcile.ResourceVersion).To(Equal(rvAfterApply),
			"Node ResourceVersion must not change after the apply path settles")
	})

	It("recovers cleanly when a prior reconcile patched the Node but failed to transition the condition", func() {
		nodeName := "node-recover-1"
		// Pre-apply the taint + label as if a prior reconcile succeeded then crashed
		// before status was written. Verifies the already-applied detection.
		Expect(r.Client.Create(ctx, newTestNode(nodeName,
			map[string]string{ManagedLabelKey: ManagedLabelValueFalse},
			[]corev1.Taint{{Key: ReleaseTaintKey, Value: "recover-err-1", Effect: corev1.TaintEffectNoSchedule}}))).
			To(Succeed())
		DeferCleanup(deleteNodeForCleanup, ctx, r, nodeName)

		errObj := newTestERR("recover-err-1", nodeName)
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)

		released := findERRCondition(got, ConditionNVSentinelOwnershipReleased)
		Expect(released.Status).To(Equal("True"))
		Expect(released.Reason).To(Equal(ReasonReleaseTaintApplied))
		Expect(released.Message).To(ContainSubstring("already present"),
			"already-applied message should call out the recovery case for operator visibility")
	})

	It("requeues without transitioning when the target Node does not exist", func() {
		errObj := newTestERR("missing-node-err-1", "node-does-not-exist")
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}

		// reconcileInitialize uses two passes (finalizer Update, then status Patch).
		// Drive both to completion before checking branch 3 behavior.
		for i := 0; i < 2; i++ {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred(), "init pass %d", i+1)
		}

		// Third reconcile hits branch 3, finds the Node missing, requeues.
		result, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred(), "missing Node must not propagate as a reconcile error")
		Expect(result.RequeueAfter).To(Equal(nodeMissingRequeue), "missing Node must requeue, not fail")

		var got nvsentinelv1.ExternalRemediationRequest
		Expect(r.Client.Get(ctx, key, &got)).To(Succeed())
		released := findERRCondition(&got, ConditionNVSentinelOwnershipReleased)
		Expect(released.Status).To(Equal("Unknown"),
			"missing Node must leave NVSentinelOwnershipReleased Unknown for retry")
		Expect(released.Reason).To(Equal(reasonInitializing))
	})

	It("transitions to False when the spec.healthEvent.nodeName is empty", func() {
		errObj := newTestERR("empty-node-err-1", "")
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)

		released := findERRCondition(got, ConditionNVSentinelOwnershipReleased)
		Expect(released.Status).To(Equal("False"))
		Expect(released.Reason).To(Equal(ReasonReleaseTaintFailed))
		Expect(released.Message).To(ContainSubstring("nodeName is empty"))
	})

	It("transitions to False when the Node is already tainted by a different ERR (drift)", func() {
		nodeName := "node-drift-1"
		// Pre-apply the taint with a DIFFERENT ERR's name as the value.
		Expect(r.Client.Create(ctx, newTestNode(nodeName, nil,
			[]corev1.Taint{{Key: ReleaseTaintKey, Value: "some-other-err", Effect: corev1.TaintEffectNoSchedule}}))).
			To(Succeed())
		DeferCleanup(deleteNodeForCleanup, ctx, r, nodeName)

		errObj := newTestERR("drift-err-1", nodeName)
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)

		released := findERRCondition(got, ConditionNVSentinelOwnershipReleased)
		Expect(released.Status).To(Equal("False"), "drift must transition condition to False")
		Expect(released.Reason).To(Equal(ReasonReleaseTaintFailed))
		Expect(released.Message).To(ContainSubstring("some-other-err"),
			"drift message must identify the existing taint owner")
		Expect(released.Message).To(ContainSubstring(nodeName))

		// Node taint must be unchanged — we don't overwrite another ERR's claim.
		var node corev1.Node
		Expect(r.Client.Get(ctx, ctrlclient.ObjectKey{Name: nodeName}, &node)).To(Succeed())
		taint := findTaintByKey(node.Spec.Taints, ReleaseTaintKey)
		Expect(taint).NotTo(BeNil())
		Expect(taint.Value).To(Equal("some-other-err"), "drift case must NOT overwrite the existing taint")
		Expect(node.Labels).NotTo(HaveKey(ManagedLabelKey), "drift case must NOT set managed=false")
	})

	It("transitions to False when the Node patch is forbidden by RBAC", func() {
		nodeName := "node-rbac-1"

		// Build a watch-capable client for both the test setup and the interceptor
		// wrap; the interceptor requires WithWatch, not the plain Client interface.
		baseClient, err := ctrlclient.NewWithWatch(cfg, ctrlclient.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		Expect(baseClient.Create(ctx, newTestNode(nodeName, nil, nil))).To(Succeed())
		DeferCleanup(deleteNodeForCleanup, ctx, r, nodeName)

		// Wrap the reconciler's client with an interceptor that turns Node PATCHes
		// into HTTP 403 Forbidden, simulating a missing RBAC binding.
		r.Client = interceptor.NewClient(baseClient, interceptor.Funcs{
			Patch: func(ctx context.Context, c ctrlclient.WithWatch, obj ctrlclient.Object,
				patch ctrlclient.Patch, opts ...ctrlclient.PatchOption,
			) error {
				if _, ok := obj.(*corev1.Node); ok {
					return apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, obj.GetName(),
						fmt.Errorf("simulated RBAC denial"))
				}

				return c.Patch(ctx, obj, patch, opts...)
			},
		})

		errObj := newTestERR("rbac-err-1", nodeName)
		Expect(r.Client.Create(ctx, errObj)).To(Succeed())
		DeferCleanup(deleteERRForCleanup, ctx, r, errObj)

		key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}
		got := reconcileToSteadyState(ctx, r, key, 3)

		released := findERRCondition(got, ConditionNVSentinelOwnershipReleased)
		Expect(released.Status).To(Equal("False"), "forbidden patch must transition condition to False")
		Expect(released.Reason).To(Equal(ReasonReleaseTaintFailed))
		Expect(released.Message).To(ContainSubstring("forbidden"))
	})
})

// TestERRReconciler_NeedsInitialization is a pure-unit test (no envtest needed),
// so it runs as a plain testing.T function alongside the ginkgo specs.
func TestERRReconciler_NeedsInitialization(t *testing.T) {
	r := &ExternalRemediationRequestReconciler{}

	t.Run("no finalizer, no conditions", func(t *testing.T) {
		errObj := newTestERR("a", "n")
		assert.True(t, r.needsInitialization(errObj))
	})

	t.Run("finalizer present, no conditions", func(t *testing.T) {
		errObj := newTestERR("a", "n")
		errObj.Finalizers = []string{ExternalRemediationFinalizer}
		assert.True(t, r.needsInitialization(errObj))
	})

	t.Run("finalizer absent, conditions present", func(t *testing.T) {
		errObj := newTestERR("a", "n")
		errObj.Status = &protos.ExternalRemediationRequestStatus{
			Conditions: []*protos.Condition{
				{Type: ConditionNVSentinelOwnershipReleased, Status: "Unknown"},
				{Type: ConditionExternalRemediationComplete, Status: "Unknown"},
			},
		}
		assert.True(t, r.needsInitialization(errObj))
	})

	t.Run("fully initialized", func(t *testing.T) {
		errObj := newTestERR("a", "n")
		errObj.Finalizers = []string{ExternalRemediationFinalizer}
		errObj.Status = &protos.ExternalRemediationRequestStatus{
			Conditions: []*protos.Condition{
				{Type: ConditionNVSentinelOwnershipReleased, Status: "Unknown"},
				{Type: ConditionExternalRemediationComplete, Status: "Unknown"},
			},
		}
		assert.False(t, r.needsInitialization(errObj))
	})

	t.Run("only one condition present", func(t *testing.T) {
		errObj := newTestERR("a", "n")
		errObj.Finalizers = []string{ExternalRemediationFinalizer}
		errObj.Status = &protos.ExternalRemediationRequestStatus{
			Conditions: []*protos.Condition{
				{Type: ConditionNVSentinelOwnershipReleased, Status: "Unknown"},
			},
		}
		assert.True(t, r.needsInitialization(errObj))
	})
}

// Test_needsInitialization_StaticChecks needs at least one require import,
// satisfying linters that flag the imported-but-unused symbol otherwise.
func Test_needsInitialization_StaticChecks(t *testing.T) {
	r := &ExternalRemediationRequestReconciler{}
	require.False(t, r.needsInitialization(&nvsentinelv1.ExternalRemediationRequest{
		ObjectMeta: metav1.ObjectMeta{
			Finalizers: []string{ExternalRemediationFinalizer},
		},
		Status: &protos.ExternalRemediationRequestStatus{
			Conditions: []*protos.Condition{
				{Type: ConditionNVSentinelOwnershipReleased, Status: "True"},
				{Type: ConditionExternalRemediationComplete, Status: "False"},
			},
		},
	}))
}

// deleteERRForCleanup removes the cleanup finalizer and deletes the ERR so the
// next test starts clean. Used as a DeferCleanup target.
func deleteERRForCleanup(ctx context.Context, r *ExternalRemediationRequestReconciler, errObj *nvsentinelv1.ExternalRemediationRequest) {
	forceFinalizerRemoval(ctx, r, errObj)
}

// forceFinalizerRemoval strips the cleanup finalizer (if present) and ensures
// the object is fully deleted from the API server so tests don't bleed state.
func forceFinalizerRemoval(ctx context.Context, r *ExternalRemediationRequestReconciler, errObj *nvsentinelv1.ExternalRemediationRequest) {
	key := ctrlclient.ObjectKey{Name: errObj.Name, Namespace: errObj.Namespace}

	var fresh nvsentinelv1.ExternalRemediationRequest
	if err := r.Client.Get(ctx, key, &fresh); err != nil {
		return
	}

	if controllerutil.RemoveFinalizer(&fresh, ExternalRemediationFinalizer) {
		_ = r.Client.Update(ctx, &fresh)
	}

	_ = r.Client.Delete(ctx, &fresh)
}

// newTestNode returns a minimal corev1.Node usable from envtest. labels/taints
// are optional; pass nil to start clean.
func newTestNode(name string, labels map[string]string, taints []corev1.Taint) *corev1.Node {
	return &corev1.Node{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Node",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: corev1.NodeSpec{
			Taints: taints,
		},
	}
}

// deleteNodeForCleanup removes a Node so tests don't bleed state. Used as a
// DeferCleanup target.
func deleteNodeForCleanup(ctx context.Context, r *ExternalRemediationRequestReconciler, nodeName string) {
	var node corev1.Node
	if err := r.Client.Get(ctx, ctrlclient.ObjectKey{Name: nodeName}, &node); err != nil {
		return
	}

	_ = r.Client.Delete(ctx, &node)
}

// findERRCondition returns the proto Condition with the given type, or nil.
func findERRCondition(errObj *nvsentinelv1.ExternalRemediationRequest, condType string) *protos.Condition {
	if errObj.Status == nil {
		return nil
	}

	for _, c := range errObj.Status.Conditions {
		if c.Type == condType {
			return c
		}
	}

	return nil
}

// snapshotConditions returns a stable string representation of the ERR's
// status conditions, sidestepping proto-message equality quirks
// (sync.Mutex in protoimpl.MessageState).
func snapshotConditions(errObj *nvsentinelv1.ExternalRemediationRequest) string {
	if errObj.Status == nil {
		return ""
	}

	var parts []string

	for _, c := range errObj.Status.Conditions {
		var ts string
		if c.LastTransitionTime != nil {
			ts = c.LastTransitionTime.AsTime().Format(time.RFC3339Nano)
		}

		parts = append(parts, c.Type+"="+c.Status+":"+c.Reason+":"+c.Message+"@"+ts)
	}

	return strings.Join(parts, "|")
}
