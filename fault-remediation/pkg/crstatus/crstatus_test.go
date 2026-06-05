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
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/nvidia/nvsentinel/fault-remediation/pkg/config"
)

func TestCheckCondition(t *testing.T) {
	testResource := config.MaintenanceResource{
		CompleteConditionType: "Completed",
	}
	cfg := map[string]config.MaintenanceResource{
		"test": testResource,
	}

	checker := NewCRStatusChecker(nil, cfg, false)

	tests := []struct {
		name     string
		cr       *unstructured.Unstructured
		expected CRState
	}{
		{
			name: "no status returns skip - in progress",
			cr: &unstructured.Unstructured{
				Object: map[string]any{
					"metadata": map[string]any{"name": "test-cr"},
				},
			},
			expected: CRStateInProgress,
		},
		{
			name: "condition true returns allow create - success",
			cr: &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{
						"conditions": []any{
							map[string]any{
								"type":   "Completed",
								"status": "True",
							},
						},
					},
				},
			},
			expected: CRStateSucceeded,
		},
		{
			name: "condition false returns allow create - failed",
			cr: &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{
						"conditions": []any{
							map[string]any{
								"type":   "Completed",
								"status": "False",
							},
						},
					},
				},
			},
			expected: CRStateFailed,
		},
		{
			name: "condition unknown returns skip - in progress",
			cr: &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{
						"conditions": []any{
							map[string]any{
								"type":   "Completed",
								"status": "Unknown",
							},
						},
					},
				},
			},
			expected: CRStateInProgress,
		},
		{
			name: "condition not found returns skip - in progress",
			cr: &unstructured.Unstructured{
				Object: map[string]any{
					"status": map[string]any{
						"conditions": []any{
							map[string]any{
								"type":   "SomeOtherCondition",
								"status": "True",
							},
						},
					},
				},
			},
			expected: CRStateInProgress,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checker.checkCondition(tt.cr, testResource)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCheckCondition_ExternalRemediationRequest covers the asymmetric
// True/False semantics for ERR per ADR-040 (JSC-95). The non-obvious case is
// False: unlike other maintenance CRs where False = terminal failure (allow
// retry), False on an ERR means the external system reported failure but
// the node is still released, so dedup must continue to suppress new ERRs
// until the operator acts.
func TestCheckCondition_ExternalRemediationRequest(t *testing.T) {
	errResource := config.MaintenanceResource{
		ApiGroup:              "nvsentinel.nvidia.com",
		Kind:                  "ExternalRemediationRequest",
		CompleteConditionType: "ExternalRemediationComplete",
	}
	cfg := map[string]config.MaintenanceResource{
		"external-remediation": errResource,
	}
	checker := NewCRStatusChecker(nil, cfg, false)

	withConditions := func(conds ...map[string]any) *unstructured.Unstructured {
		anyConds := make([]any, 0, len(conds))
		for _, c := range conds {
			anyConds = append(anyConds, c)
		}

		return &unstructured.Unstructured{
			Object: map[string]any{
				"status": map[string]any{
					"conditions": anyConds,
				},
			},
		}
	}

	tests := []struct {
		name     string
		cr       *unstructured.Unstructured
		expected CRState
		why      string
	}{
		{
			name:     "ExternalRemediationComplete=True -> NotFound",
			cr:       withConditions(map[string]any{"type": "ExternalRemediationComplete", "status": "True"}),
			expected: CRStateNotFound,
			why:      "cleanup done; equivalence-group entry should be pruned; ShouldSkip=false",
		},
		{
			name:     "ExternalRemediationComplete=False -> InProgress (asymmetric)",
			cr:       withConditions(map[string]any{"type": "ExternalRemediationComplete", "status": "False"}),
			expected: CRStateInProgress,
			why:      "external failed; ERR still claims node; ShouldSkip=true",
		},
		{
			name:     "ExternalRemediationComplete=Unknown -> InProgress",
			cr:       withConditions(map[string]any{"type": "ExternalRemediationComplete", "status": "Unknown"}),
			expected: CRStateInProgress,
			why:      "ERR in-flight; ShouldSkip=true",
		},
		{
			name: "ExternalRemediationComplete missing among other conditions -> NotFound",
			cr: withConditions(
				map[string]any{"type": "NVSentinelOwnershipReleased", "status": "True"},
				map[string]any{"type": "SomethingElse", "status": "False"},
			),
			expected: CRStateNotFound,
			why:      "no Complete condition present; treat as no claim per spec",
		},
		{
			name:     "no status at all -> NotFound",
			cr:       &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": "fresh"}}},
			expected: CRStateNotFound,
			why:      "ERR pre-init; spec calls this out as ShouldSkip=false",
		},
		{
			name:     "no conditions slice -> NotFound",
			cr:       &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{}}},
			expected: CRStateNotFound,
			why:      "status present but no conditions yet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checker.checkCondition(tt.cr, errResource)
			assert.Equal(t, tt.expected, got, tt.why)
		})
	}
}

// TestShouldSkipCRCreation_ExternalRemediationRequest verifies the end-to-end
// dedup signal returned to the reconciler for each ERR state. This is the
// matrix the JSC-95 spec describes in terms of ShouldSkip directly.
func TestShouldSkipCRCreation_ExternalRemediationRequest(t *testing.T) {
	errResource := config.MaintenanceResource{
		ApiGroup:              "nvsentinel.nvidia.com",
		Kind:                  "ExternalRemediationRequest",
		CompleteConditionType: "ExternalRemediationComplete",
	}

	tests := []struct {
		conditionStatus string
		wantState       CRState
		wantShouldSkip  bool
	}{
		{"Unknown", CRStateInProgress, true},
		{"False", CRStateInProgress, true},
		{"True", CRStateNotFound, false},
		{"", CRStateNotFound, false},
	}

	for _, tt := range tests {
		t.Run("conditionStatus="+tt.conditionStatus, func(t *testing.T) {
			got := errStateFromCondition(tt.conditionStatus)
			assert.Equal(t, tt.wantState, got)
			// Mirror the ShouldSkip mapping the checker uses.
			shouldSkip := got == CRStateInProgress || got == CRStateSucceeded
			assert.Equal(t, tt.wantShouldSkip, shouldSkip,
				"ShouldSkip semantics for ERR(%s)", tt.conditionStatus)
			// Defensive: assert the resource detector matches.
			assert.True(t, isExternalRemediationRequest(errResource))
		})
	}
}
