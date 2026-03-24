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

package evaluator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

func TestHealthEventRuleEvaluator_Evaluate(t *testing.T) {
	expression := "event.agent == 'GPU' && event.checkName == 'XidError' && ('31' in event.errorCode || '42' in event.errorCode)"

	evaluator, err := NewHealthEventRuleEvaluator(expression)
	require.NoError(t, err)

	tests := []struct {
		name     string
		event    *protos.HealthEvent
		expected EvaluationResult
	}{
		{
			name: "matching event",
			event: &protos.HealthEvent{
				Agent:     "GPU",
				CheckName: "XidError",
				ErrorCode: []string{"31"},
			},
			expected: EvaluationSuccess,
		},
		{
			name: "non-matching agent",
			event: &protos.HealthEvent{
				Agent:     "CPU",
				CheckName: "XidError",
				ErrorCode: []string{"31"},
			},
			expected: EvaluationFailed,
		},
		{
			name: "non-matching error code",
			event: &protos.HealthEvent{
				Agent:     "GPU",
				CheckName: "XidError",
				ErrorCode: []string{"99"},
			},
			expected: EvaluationFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(tc.event)
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestHealthEventRuleEvaluator_InvalidExpression(t *testing.T) {
	_, err := NewHealthEventRuleEvaluator("invalid syntax !!!")
	assert.Error(t, err)
}

func TestRoundTrip(t *testing.T) {
	event := &protos.HealthEvent{
		Agent:     "GPU",
		CheckName: "XidError",
		ErrorCode: []string{"31", "42"},
		NodeName:  "node-1",
		Metadata:  map[string]string{"key": "value"},
	}

	result, err := RoundTrip(event)
	require.NoError(t, err)

	assert.Equal(t, "GPU", result["agent"])
	assert.Equal(t, "XidError", result["checkName"])
	assert.Equal(t, "node-1", result["nodeName"])
}
