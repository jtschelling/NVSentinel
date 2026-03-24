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

// Package evaluator provides shared CEL-based rule evaluation for NVSentinel components.
package evaluator

import (
	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

// EvaluationResult represents the result of a rule evaluation.
type EvaluationResult int

const (
	EvaluationSuccess EvaluationResult = iota
	EvaluationFailed
)

// HealthEventEvaluator evaluates a CEL expression against a HealthEvent.
type HealthEventEvaluator interface {
	Evaluate(healthEvent *protos.HealthEvent) (EvaluationResult, error)
}
