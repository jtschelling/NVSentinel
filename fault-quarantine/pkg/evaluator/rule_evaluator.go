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
	"k8s.io/apimachinery/pkg/runtime"
	corelisters "k8s.io/client-go/listers/core/v1"

	celevaluator "github.com/nvidia/nvsentinel/cel-evaluator/pkg/evaluator"
	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/fault-quarantine/pkg/common"
)

const nodeObjKey = "node"

// RuleEvaluator is the interface for evaluating rules against HealthEvents.
type RuleEvaluator interface {
	Evaluate(healthEvent *protos.HealthEvent) (common.RuleEvaluationResult, error)
}

// healthEventRuleAdapter wraps the shared HealthEventRuleEvaluator to implement
// the local RuleEvaluator interface with common.RuleEvaluationResult.
type healthEventRuleAdapter struct {
	inner *celevaluator.HealthEventRuleEvaluator
}

// NewHealthEventRuleEvaluator creates a new HealthEventRuleEvaluator using the shared cel-evaluator module.
func NewHealthEventRuleEvaluator(expression string) (RuleEvaluator, error) {
	eval, err := celevaluator.NewHealthEventRuleEvaluator(expression)
	if err != nil {
		return nil, err
	}

	return &healthEventRuleAdapter{inner: eval}, nil
}

func (a *healthEventRuleAdapter) Evaluate(event *protos.HealthEvent) (common.RuleEvaluationResult, error) {
	result, err := a.inner.Evaluate(event)
	if err != nil {
		return common.RuleEvaluationFailed, err
	}

	if result == celevaluator.EvaluationSuccess {
		return common.RuleEvaluationSuccess, nil
	}

	return common.RuleEvaluationFailed, nil
}

type NodeRuleEvaluator struct {
	expression string
	program    cel.Program
	nodeLister corelisters.NodeLister
}

// NewNodeRuleEvaluator creates a new NodeRuleEvaluator
func NewNodeRuleEvaluator(expression string, nodeLister corelisters.NodeLister) (*NodeRuleEvaluator, error) {
	slog.Info("Creating NodeRuleEvaluator", "expression", expression)

	// Create a CEL environment with declarations for node.labels and node.annotations
	env, err := cel.NewEnv(
		cel.Variable(nodeObjKey, cel.AnyType),
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

	return &NodeRuleEvaluator{
		expression: expression,
		program:    program,
		nodeLister: nodeLister,
	}, nil
}

// Evaluate the CEL expression against node metadata (labels and annotations)
func (nm *NodeRuleEvaluator) Evaluate(event *protos.HealthEvent) (common.RuleEvaluationResult, error) {
	slog.Info("Evaluating NodeRuleEvaluator for node", "node", event.NodeName)

	nodeInfo, err := nm.getNode(event.NodeName)
	if err != nil {
		return common.RuleEvaluationFailed, fmt.Errorf("failed to get node metadata: %w", err)
	}

	out, _, err := nm.program.Eval(nodeInfo)
	if err != nil {
		return common.RuleEvaluationFailed, fmt.Errorf("failed to evaluate expression: %w", err)
	}

	result, ok := out.Value().(bool)
	if !ok {
		return common.RuleEvaluationFailed, fmt.Errorf("expression did not return a boolean: %v", out)
	}

	if result {
		return common.RuleEvaluationSuccess, nil
	}

	return common.RuleEvaluationFailed, nil
}

// getNode gets both labels and annotations from a node using the informer lister
func (nm *NodeRuleEvaluator) getNode(nodeName string) (map[string]interface{}, error) {
	node, err := nm.nodeLister.Get(nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to get node %s from informer cache: %w", nodeName, err)
	}

	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(node)
	if err != nil {
		return nil, fmt.Errorf("failed to convert node %s to unstructured: %w", nodeName, err)
	}

	return map[string]interface{}{
		"node": unstructuredObj,
	}, nil
}
