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

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"

	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

const eventObjKey = "event"

// HealthEventRuleEvaluator evaluates a CEL expression against a HealthEvent.
type HealthEventRuleEvaluator struct {
	expression string
	program    cel.Program
}

// NewHealthEventRuleEvaluator creates a new HealthEventRuleEvaluator with dynamic declarations.
func NewHealthEventRuleEvaluator(expression string) (*HealthEventRuleEvaluator, error) {
	slog.Info("Creating HealthEventRuleEvaluator", "expression", expression)

	env, err := cel.NewEnv(
		cel.Variable(eventObjKey, cel.AnyType),
		ext.Strings(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	ast, issues := env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to parse expression: %w", issues.Err())
	}

	checkedAst, issues := env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to check expression: %w", issues.Err())
	}

	program, err := env.Program(checkedAst)
	if err != nil {
		return nil, fmt.Errorf("failed to compile expression: %w", err)
	}

	return &HealthEventRuleEvaluator{
		expression: expression,
		program:    program,
	}, nil
}

// Evaluate evaluates the CEL expression against the provided HealthEvent.
func (he *HealthEventRuleEvaluator) Evaluate(
	event *protos.HealthEvent,
) (EvaluationResult, error) {
	obj, err := RoundTrip(event)
	if err != nil {
		return EvaluationFailed, fmt.Errorf("error roundtripping event: %w", err)
	}

	out, _, err := he.program.Eval(map[string]interface{}{
		eventObjKey: obj,
	})
	if err != nil {
		return EvaluationFailed, fmt.Errorf("failed to evaluate expression: %w", err)
	}

	result, ok := out.Value().(bool)
	if !ok {
		return EvaluationFailed, fmt.Errorf("expression did not return a boolean: %v", out)
	}

	if result {
		return EvaluationSuccess, nil
	}

	return EvaluationFailed, nil
}
