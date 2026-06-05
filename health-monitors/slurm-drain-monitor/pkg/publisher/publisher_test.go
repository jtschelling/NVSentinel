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
	"github.com/nvidia/nvsentinel/health-monitors/slurm-drain-monitor/pkg/parser"
)

type fakePlatformConnectorClient struct {
	events *pb.HealthEvents
}

func (f *fakePlatformConnectorClient) HealthEventOccurredV1(
	_ context.Context, events *pb.HealthEvents, _ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	f.events = events
	return &emptypb.Empty{}, nil
}

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

// TestPublishDrainEvents_GatedOnManagedFalse verifies the ADR-040 emission
// gate: when the target Node carries nvsentinel.dgxc.nvidia.com/managed=false,
// the publisher MUST drop the event without contacting the platform connector.
func TestPublishDrainEvents_GatedOnManagedFalse(t *testing.T) {
	tests := []struct {
		name       string
		nodeLabels map[string]string
		expectEmit bool
	}{
		{"managed=false drops the event", map[string]string{managed.ManagedLabelKey: managed.ManagedLabelValueFalse}, false},
		{"managed=true emits normally", map[string]string{managed.ManagedLabelKey: "true"}, true},
		{"managed label absent emits normally", nil, true},
		{"managed=<typo> emits normally", map[string]string{managed.ManagedLabelKey: "False"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakePlatformConnectorClient{}
			lister := nodeListerWith(t, &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node-1", Labels: tt.nodeLabels},
			})

			pub := New(client, "passthrough:///platform-connector",
				pb.ProcessingStrategy_EXECUTE_REMEDIATION, lister)

			reasons := []parser.MatchedReason{{PatternName: "test", CheckName: "test", Message: "test"}}
			err := pub.PublishDrainEvents(context.Background(), reasons, "node-1", false, "ns", "pod")
			require.NoError(t, err)

			if tt.expectEmit {
				assert.NotNil(t, client.events, "expected emission")
			} else {
				assert.Nil(t, client.events, "expected gate to drop the event")
			}
		})
	}
}

// TestPublishDrainEvents_FailsOpenForUnknownNode covers the cache-cold case:
// during informer warmup, an unknown node must NOT be silenced.
func TestPublishDrainEvents_FailsOpenForUnknownNode(t *testing.T) {
	client := &fakePlatformConnectorClient{}
	lister := nodeListerWith(t) // empty cache

	pub := New(client, "passthrough:///platform-connector",
		pb.ProcessingStrategy_EXECUTE_REMEDIATION, lister)

	reasons := []parser.MatchedReason{{PatternName: "test", CheckName: "test", Message: "test"}}
	err := pub.PublishDrainEvents(context.Background(), reasons, "unknown-node", false, "ns", "pod")
	require.NoError(t, err)
	assert.NotNil(t, client.events,
		"unknown node must NOT be silenced; the gate is fail-open during informer warmup")
}
