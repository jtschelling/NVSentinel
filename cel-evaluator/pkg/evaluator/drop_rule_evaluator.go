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
	"fmt"
	"log/slog"

	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

// DropRuleEvaluator evaluates a set of CEL expressions against HealthEvents using ANY/OR
// semantics. If any expression matches, the event should be dropped.
type DropRuleEvaluator struct {
	evaluators []*HealthEventRuleEvaluator
}

// NewDropRuleEvaluator creates a DropRuleEvaluator from a list of CEL expressions.
// Returns an error if any expression fails to compile.
func NewDropRuleEvaluator(expressions []string) (*DropRuleEvaluator, error) {
	evaluators := make([]*HealthEventRuleEvaluator, 0, len(expressions))

	for _, expr := range expressions {
		eval, err := NewHealthEventRuleEvaluator(expr)
		if err != nil {
			return nil, fmt.Errorf("failed to compile drop rule expression %q: %w", expr, err)
		}

		evaluators = append(evaluators, eval)
	}

	return &DropRuleEvaluator{evaluators: evaluators}, nil
}

// ShouldDrop returns true if any drop rule expression matches the given HealthEvent.
// On evaluation errors, returns false (fail open) and logs the error.
func (d *DropRuleEvaluator) ShouldDrop(event *protos.HealthEvent) (bool, error) {
	for _, eval := range d.evaluators {
		result, err := eval.Evaluate(event)
		if err != nil {
			slog.Error("Error evaluating drop rule", "expression", eval.expression, "error", err)
			continue
		}

		if result == EvaluationSuccess {
			return true, nil
		}
	}

	return false, nil
}
