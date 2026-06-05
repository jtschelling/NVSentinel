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
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
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
