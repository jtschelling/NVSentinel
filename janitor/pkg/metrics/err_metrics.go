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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// ERR observability metric series. The full schema is specified by ADR-040's
// Observability section and JSC-98's acceptance criteria. The names share the
// nvsentinel_external_remediation_ namespace so a single grep against the
// metrics endpoint surfaces the entire ERR signal set.

// ERR phase labels for err_total.
const (
	ERRPhaseCreated          = "created"
	ERRPhaseReleased         = "released"
	ERRPhaseExternalResponse = "external_response"
	ERRPhaseClosed           = "closed"
)

// ERR result labels for err_total / err_age_seconds.
const (
	ERRResultSuccess         = "success"
	ERRResultFailure         = "failure"
	ERRResultOperatorDeleted = "operator_deleted"
)

// ERR open-state labels for err_open.
const (
	ERROpenStateAwaiting = "awaiting"
	ERROpenStateFailed   = "failed"
)

var (
	// ERRTotal counts ERR lifecycle transitions.
	//
	// phase=created       : reconciler initialised a fresh ERR (added the finalizer and Unknown conditions).
	// phase=released      : NVSentinelOwnershipReleased transitioned (apply path). result=success|failure.
	// phase=external_response : ExternalRemediationComplete observed True or False. result=success|failure.
	// phase=closed        : cleanup ran (taint+label removed). result=success (Complete=True) | operator_deleted (kubectl delete err).
	ERRTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nvsentinel_external_remediation_err_total",
		Help: "Lifecycle transitions of ExternalRemediationRequest objects, labeled by phase and outcome.",
	}, []string{"phase", "result"})

	// ERROpen tracks the number of currently-open ERRs, partitioned by the
	// substate they're sitting in (awaiting external response vs. external
	// reported failure but operator hasn't intervened yet).
	ERROpen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "nvsentinel_external_remediation_err_open",
		Help: "Currently-open ExternalRemediationRequest objects by node, action, and substate.",
	}, []string{"node", "recommended_action", "state"})

	// ERRAgeSeconds records how long an ERR was open (creation to close)
	// labeled by which cleanup path closed it. Bucket choice spans seconds
	// (a healthy auto-cleanup) through hours (an ERR that an operator forgot
	// about).
	ERRAgeSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "nvsentinel_external_remediation_err_age_seconds",
		Help:    "Age of an ExternalRemediationRequest at close time.",
		Buckets: prometheus.ExponentialBuckets(10, 4, 8), // 10s, 40s, 160s, ~10m, ~43m, ~3h, ~11h, ~46h
	}, []string{"recommended_action", "result"})
)

// IncERRTotal increments the ERR lifecycle counter for the given phase /
// result. result may be "" for phase=created (no per-result split).
func (m *ActionMetrics) IncERRTotal(phase, result string) {
	ERRTotal.With(prometheus.Labels{
		"phase":  phase,
		"result": result,
	}).Inc()
}

// AdjustERROpen changes the open-ERR gauge for the (node, action, state)
// triple by delta. delta=+1 when the ERR enters the state, delta=-1 when it
// leaves. The reconciler keeps the bookkeeping in one place per branch to
// avoid double-counting across re-reconciles.
func (m *ActionMetrics) AdjustERROpen(node, recommendedAction, state string, delta float64) {
	ERROpen.With(prometheus.Labels{
		"node":               node,
		"recommended_action": recommendedAction,
		"state":              state,
	}).Add(delta)
}

// ObserveERRAge records the age of an ERR that just closed via the named
// result path.
func (m *ActionMetrics) ObserveERRAge(recommendedAction, result string, ageSeconds float64) {
	ERRAgeSeconds.With(prometheus.Labels{
		"recommended_action": recommendedAction,
		"result":             result,
	}).Observe(ageSeconds)
}

// registerERRMetrics registers the ERR observables with the controller-runtime
// metrics registry. Called from NewActionMetrics so the standard janitor
// metrics-server endpoint exposes them without further wiring.
func registerERRMetrics() {
	metrics.Registry.MustRegister(
		ERRTotal,
		ERROpen,
		ERRAgeSeconds,
	)
}
