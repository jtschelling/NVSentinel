// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
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
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	listersv1 "k8s.io/client-go/listers/core/v1"

	"github.com/nvidia/nvsentinel/commons/pkg/healthpub"
	"github.com/nvidia/nvsentinel/commons/pkg/managed"
	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/health-monitors/kubernetes-object-monitor/pkg/config"
	"github.com/nvidia/nvsentinel/health-monitors/kubernetes-object-monitor/pkg/metrics"
)

const (
	agentName = "kubernetes-object-monitor"
)

// Publisher publishes health events to the platform connector via the
// shared healthpub publisher (commons/pkg/healthpub).
//
// Per ADR-040 the emission path is gated on the nvsentinel.dgxc.nvidia.com/managed
// label: when a Node carries "false", the operator has handed it to an
// external system and this monitor must stop emitting events targeting it.
// The check sits at emission time (not at observation time) so we keep
// historical visibility into what was happening on a released node.
type Publisher struct {
	pub                *healthpub.Publisher
	processingStrategy pb.ProcessingStrategy
	nodeLister         listersv1.NodeLister
}

// New constructs a Publisher. target must match the gRPC target string
// used to dial client (typically "unix:///var/run/nvsentinel.sock").
// nodeLister is consulted on every PublishHealthEvent to honour the
// managed=false opt-out from ADR-040. A nil lister disables the gate
// (fail-open) which is the right default during early startup.
func New(client pb.PlatformConnectorClient, target string, processingStrategy pb.ProcessingStrategy,
	nodeLister listersv1.NodeLister) *Publisher {
	return &Publisher{
		pub:                healthpub.New(client, target, agentName),
		processingStrategy: processingStrategy,
		nodeLister:         nodeLister,
	}
}

// PublishHealthEvent publishes a health event to the platform connector.
// The resourceInfo parameter is used to populate the entitiesImpacted field,
// which allows fault-quarantine to track each resource individually.
func (p *Publisher) PublishHealthEvent(ctx context.Context,
	policy *config.Policy, nodeName string, isHealthy bool, resourceInfo *config.ResourceInfo) error {
	if managed.IsNodeOptedOut(ctx, p.nodeLister, nodeName) {
		metrics.EmissionsSkippedManaged.WithLabelValues(policy.Name).Inc()
		slog.Debug("Skipping health event for managed=false node",
			"node", nodeName, "policy", policy.Name)

		return nil
	}

	strategy := p.processingStrategy

	if policy.HealthEvent.ProcessingStrategy != "" {
		value, ok := pb.ProcessingStrategy_value[policy.HealthEvent.ProcessingStrategy]
		if !ok {
			return fmt.Errorf("unexpected processingStrategy value: %q", policy.HealthEvent.ProcessingStrategy)
		}

		strategy = pb.ProcessingStrategy(value)
	}

	// Build entitiesImpacted from resource info

	var entitiesImpacted []*pb.Entity

	if resourceInfo != nil {
		entityValue := resourceInfo.Name
		if resourceInfo.Namespace != "" {
			entityValue = fmt.Sprintf("%s/%s", resourceInfo.Namespace, resourceInfo.Name)
		}

		entitiesImpacted = []*pb.Entity{
			{
				EntityType:  resourceInfo.GVK(),
				EntityValue: entityValue,
			},
		}
	}

	quarantineOverrides := behaviourOverridesFromSpec(policy.HealthEvent.QuarantineOverrides)
	drainOverrides := behaviourOverridesFromSpec(policy.HealthEvent.DrainOverrides)

	event := &pb.HealthEvent{
		Version:             1,
		Agent:               agentName,
		CheckName:           policy.Name,
		ComponentClass:      policy.HealthEvent.ComponentClass,
		GeneratedTimestamp:  timestamppb.New(time.Now()),
		Message:             policy.HealthEvent.Message,
		IsFatal:             policy.HealthEvent.IsFatal,
		IsHealthy:           isHealthy,
		NodeName:            nodeName,
		RecommendedAction:   mapRecommendedAction(policy.HealthEvent.RecommendedAction),
		ErrorCode:           policy.HealthEvent.ErrorCode,
		ProcessingStrategy:  strategy,
		EntitiesImpacted:    entitiesImpacted,
		QuarantineOverrides: quarantineOverrides,
		DrainOverrides:      drainOverrides,
	}

	healthEvents := &pb.HealthEvents{
		Version: 1,
		Events:  []*pb.HealthEvent{event},
	}

	slog.Info("Publishing health event", "event", event)

	return p.pub.Publish(ctx, healthEvents)
}

func mapRecommendedAction(action string) pb.RecommendedAction {
	if value, exists := pb.RecommendedAction_value[action]; exists {
		return pb.RecommendedAction(value)
	}

	return pb.RecommendedAction_CONTACT_SUPPORT
}

func behaviourOverridesFromSpec(spec *config.BehaviourOverridesSpec) *pb.BehaviourOverrides {
	if spec == nil {
		return nil
	}

	return &pb.BehaviourOverrides{
		Force: spec.Force,
		Skip:  spec.Skip,
	}
}
