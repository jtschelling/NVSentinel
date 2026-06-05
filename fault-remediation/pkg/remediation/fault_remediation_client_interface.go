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

package remediation

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/annotation"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/common"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/config"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/crstatus"
	"github.com/nvidia/nvsentinel/fault-remediation/pkg/events"
)

type FaultRemediationClientInterface interface {
	CreateMaintenanceResource(ctx context.Context, healthEventData *events.HealthEventData,
		groupConfig *common.EquivalenceGroupConfig) (string, error)
	RunLogCollectorJob(ctx context.Context, nodeName string, eventId string) (ctrl.Result, error)
	GetAnnotationManager() annotation.NodeAnnotationManagerInterface
	GetStatusChecker() crstatus.CRStatusCheckerInterface
	GetConfig() *config.TomlConfig
}

// TemplateData holds the data to be inserted into the template
type TemplateData struct {
	// Node and event data
	NodeName                 string
	ImpactedEntityScopeValue string
	HealthEventID            string
	RecommendedAction        protos.RecommendedAction
	RecommendedActionName    string
	TraceID                  string
	SpanID                   string

	HealthEvent *protos.HealthEvent

	// CRName is the deterministic CR name for this (nodeName, healthEventID)
	// pair. For most CRD kinds the existing template-side
	// `maintenance-{NodeName}-{HealthEventID}` pattern is fine, but the ERR
	// CRD requires a name that's also a valid Kubernetes taint value
	// (≤63 chars, [a-z0-9._-]) because the ERR reconciler uses the name as
	// the value of the release taint. Templates may use this field instead
	// of inlining the {{ .HealthEvent.NodeName }}-{{ .HealthEventID }} pattern.
	CRName string

	// HealthEventJSON is the protojson encoding of the HealthEvent. Templates
	// that need to embed the full event (e.g. ERR's spec.healthEvent) inline
	// this as a YAML/JSON object value rather than templating every proto
	// field by hand. Empty for non-ERR actions where the template doesn't
	// need it.
	HealthEventJSON string

	// CRD routing metadata (populated from MaintenanceResource)
	ApiGroup  string
	Version   string
	Kind      string
	Namespace string
}
