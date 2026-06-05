// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package publisher

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	listersv1 "k8s.io/client-go/listers/core/v1"

	"github.com/nvidia/nvsentinel/commons/pkg/managed"
	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/health-monitors/kubernetes-object-monitor/pkg/config"
)

// nodeListerWith builds an informer-backed NodeLister pre-populated with the
// given Nodes for the gate tests.
func nodeListerWith(t *testing.T, nodes ...*corev1.Node) listersv1.NodeLister {
	t.Helper()

	factory := informers.NewSharedInformerFactory(fake.NewSimpleClientset(), 0)
	informer := factory.Core().V1().Nodes().Informer()

	for _, n := range nodes {
		require.NoError(t, informer.GetStore().Add(n))
	}

	return factory.Core().V1().Nodes().Lister()
}

type fakePlatformConnectorClient struct {
	events *pb.HealthEvents
}

func (f *fakePlatformConnectorClient) HealthEventOccurredV1(
	_ context.Context, events *pb.HealthEvents, _ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	f.events = events

	return &emptypb.Empty{}, nil
}

func TestPublishHealthEventIncludesBehaviourOverrides(t *testing.T) {
	client := &fakePlatformConnectorClient{}
	pub := New(client, "passthrough:///platform-connector", pb.ProcessingStrategy_EXECUTE_REMEDIATION, nil)

	policy := &config.Policy{
		Name: "operator-pod-unhealthy",
		HealthEvent: config.HealthEventSpec{
			ComponentClass:    "Software",
			IsFatal:           true,
			Message:           "operator pod is unhealthy",
			RecommendedAction: "CONTACT_SUPPORT",
			ErrorCode:         []string{"OPERATOR_POD_UNHEALTHY"},
			QuarantineOverrides: &config.BehaviourOverridesSpec{
				Force: true,
			},
			DrainOverrides: &config.BehaviourOverridesSpec{
				Skip: true,
			},
		},
	}

	err := pub.PublishHealthEvent(context.Background(), policy, "node-1", false, nil)
	require.NoError(t, err)
	require.NotNil(t, client.events)
	require.Len(t, client.events.Events, 1)

	event := client.events.Events[0]
	require.NotNil(t, event.QuarantineOverrides)
	require.True(t, event.QuarantineOverrides.Force)
	require.False(t, event.QuarantineOverrides.Skip)
	require.NotNil(t, event.DrainOverrides)
	require.False(t, event.DrainOverrides.Force)
	require.True(t, event.DrainOverrides.Skip)
}

// TestPublishHealthEvent_GatedOnManagedFalse verifies the ADR-040 emission
// gate: when the target Node carries nvsentinel.dgxc.nvidia.com/managed=false,
// the publisher MUST drop the event without contacting the platform
// connector.
func TestPublishHealthEvent_GatedOnManagedFalse(t *testing.T) {
	tests := []struct {
		name        string
		nodeLabels  map[string]string
		expectEmit  bool
		description string
	}{
		{
			name:        "managed=false drops the event",
			nodeLabels:  map[string]string{managed.ManagedLabelKey: managed.ManagedLabelValueFalse},
			expectEmit:  false,
			description: "ADR-040 opt-out: external system owns the node",
		},
		{
			name:        "managed=true emits normally",
			nodeLabels:  map[string]string{managed.ManagedLabelKey: "true"},
			expectEmit:  true,
			description: "explicit managed=true is the default",
		},
		{
			name:        "managed label absent emits normally",
			nodeLabels:  nil,
			expectEmit:  true,
			description: "default behaviour preserved when no opt-out is in place",
		},
		{
			name:        "managed=<typo> emits normally",
			nodeLabels:  map[string]string{managed.ManagedLabelKey: "False"},
			expectEmit:  true,
			description: "only canonical lowercase false opts out",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakePlatformConnectorClient{}
			lister := nodeListerWith(t, &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: tt.nodeLabels},
			})

			pub := New(client, "passthrough:///platform-connector",
				pb.ProcessingStrategy_EXECUTE_REMEDIATION, lister)

			policy := &config.Policy{
				Name:        "test-policy",
				HealthEvent: config.HealthEventSpec{Message: "test", IsFatal: false},
			}

			err := pub.PublishHealthEvent(context.Background(), policy, "node-1", false, nil)
			require.NoError(t, err)

			if tt.expectEmit {
				assert.NotNil(t, client.events, tt.description)
			} else {
				assert.Nil(t, client.events, tt.description)
			}
		})
	}
}

// TestPublishHealthEvent_FailsOpenForUnknownNode covers the cache-cold case
// during informer warmup: when the lister does not yet know about the named
// node, the gate must NOT silence emission (fail-open per ADR-040).
func TestPublishHealthEvent_FailsOpenForUnknownNode(t *testing.T) {
	client := &fakePlatformConnectorClient{}
	lister := nodeListerWith(t) // empty cache

	pub := New(client, "passthrough:///platform-connector",
		pb.ProcessingStrategy_EXECUTE_REMEDIATION, lister)

	policy := &config.Policy{
		Name:        "test-policy",
		HealthEvent: config.HealthEventSpec{Message: "test", IsFatal: false},
	}

	err := pub.PublishHealthEvent(context.Background(), policy, "node-not-yet-in-cache", false, nil)
	require.NoError(t, err)
	assert.NotNil(t, client.events,
		"unknown node must NOT be silenced; the gate is fail-open during informer warmup")
}
