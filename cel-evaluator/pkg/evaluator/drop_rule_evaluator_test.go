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

func TestDropRuleEvaluator_NoRules(t *testing.T) {
	eval, err := NewDropRuleEvaluator([]string{})
	require.NoError(t, err)

	shouldDrop, err := eval.ShouldDrop(&protos.HealthEvent{
		Agent:     "GPU",
		CheckName: "XidError",
	})
	assert.NoError(t, err)
	assert.False(t, shouldDrop)
}

func TestDropRuleEvaluator_SingleMatchingRule(t *testing.T) {
	eval, err := NewDropRuleEvaluator([]string{
		"event.agent == 'GPU'",
	})
	require.NoError(t, err)

	shouldDrop, err := eval.ShouldDrop(&protos.HealthEvent{
		Agent: "GPU",
	})
	assert.NoError(t, err)
	assert.True(t, shouldDrop)
}

func TestDropRuleEvaluator_SingleNonMatchingRule(t *testing.T) {
	eval, err := NewDropRuleEvaluator([]string{
		"event.agent == 'GPU'",
	})
	require.NoError(t, err)

	shouldDrop, err := eval.ShouldDrop(&protos.HealthEvent{
		Agent: "CPU",
	})
	assert.NoError(t, err)
	assert.False(t, shouldDrop)
}

func TestDropRuleEvaluator_MultipleRulesOneMatches(t *testing.T) {
	eval, err := NewDropRuleEvaluator([]string{
		"event.agent == 'CPU'",
		"event.agent == 'GPU'",
	})
	require.NoError(t, err)

	shouldDrop, err := eval.ShouldDrop(&protos.HealthEvent{
		Agent: "GPU",
	})
	assert.NoError(t, err)
	assert.True(t, shouldDrop)
}

func TestDropRuleEvaluator_MultipleRulesNoneMatch(t *testing.T) {
	eval, err := NewDropRuleEvaluator([]string{
		"event.agent == 'CPU'",
		"event.agent == 'DISK'",
	})
	require.NoError(t, err)

	shouldDrop, err := eval.ShouldDrop(&protos.HealthEvent{
		Agent: "GPU",
	})
	assert.NoError(t, err)
	assert.False(t, shouldDrop)
}

func TestDropRuleEvaluator_InvalidExpression(t *testing.T) {
	_, err := NewDropRuleEvaluator([]string{
		"invalid syntax !!!",
	})
	assert.Error(t, err)
}

func TestDropRuleEvaluator_ComplexExpression(t *testing.T) {
	eval, err := NewDropRuleEvaluator([]string{
		"event.recommendedAction == 5.0 && !('MANUAL_TRIGGER' in event.errorCode)",
	})
	require.NoError(t, err)

	// CONTACT_SUPPORT = 5, without MANUAL_TRIGGER -> should drop
	shouldDrop, err := eval.ShouldDrop(&protos.HealthEvent{
		RecommendedAction: protos.RecommendedAction_CONTACT_SUPPORT,
		ErrorCode:         []string{"OSMO_ERROR"},
	})
	assert.NoError(t, err)
	assert.True(t, shouldDrop)

	// CONTACT_SUPPORT = 5, with MANUAL_TRIGGER -> should NOT drop
	shouldDrop, err = eval.ShouldDrop(&protos.HealthEvent{
		RecommendedAction: protos.RecommendedAction_CONTACT_SUPPORT,
		ErrorCode:         []string{"MANUAL_TRIGGER"},
	})
	assert.NoError(t, err)
	assert.False(t, shouldDrop)
}
