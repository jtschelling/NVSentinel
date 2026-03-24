//go:build amd64_group
// +build amd64_group

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

package tests

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"tests/helpers"
)

// TestDropRulesAllowMatchingEvent verifies that a CONTACT_SUPPORT health event
// with the MANUAL_SUPPORT_REQUEST errorCode passes through the drop rule and
// triggers full remediation (quarantine → drain → RebootNode CR).
func TestDropRulesAllowMatchingEvent(t *testing.T) {
	feature := features.New("TestDropRulesAllowMatchingEvent").
		WithLabel("suite", "drop-rules")

	var testCtx *helpers.RemediationTestContext

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		var newCtx context.Context
		newCtx, testCtx = helpers.SetupFaultRemediationTest(ctx, t, c, "")
		return newCtx
	})

	feature.Assess("CONTACT_SUPPORT with allowed errorCode creates RebootNode CR", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		// Send a CONTACT_SUPPORT event with MANUAL_SUPPORT_REQUEST errorCode.
		// This should pass through the drop rule because the drop rule only
		// drops events that do NOT have MANUAL_SUPPORT_REQUEST.
		t.Log("Sending CONTACT_SUPPORT event with MANUAL_SUPPORT_REQUEST errorCode")
		event := helpers.NewHealthEvent(testCtx.NodeName).
			WithAgent("kubernetes-object-monitor").
			WithCheckName("manual-support-request").
			WithComponentClass("Node").
			WithMessage("Manual support request via node annotation").
			WithRecommendedAction(5). // CONTACT_SUPPORT
			WithErrorCode("MANUAL_SUPPORT_REQUEST").
			WithEntity("v1/Node", testCtx.NodeName)
		helpers.SendHealthEvent(ctx, t, event)

		t.Log("Waiting for node to be cordoned (quarantined)")
		require.Eventually(t, func() bool {
			node, err := helpers.GetNodeByName(ctx, client, testCtx.NodeName)
			if err != nil {
				return false
			}
			return node.Spec.Unschedulable
		}, helpers.EventuallyWaitTimeout, helpers.WaitInterval)
		t.Log("Node cordoned successfully")

		t.Log("Waiting for RebootNode CR to be created")
		cr := helpers.WaitForCR(ctx, t, client, testCtx.NodeName, helpers.RebootNodeGVK)
		t.Logf("RebootNode CR created and completed: %s", cr.GetName())

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		return helpers.TeardownFaultRemediation(ctx, t, c)
	})

	testEnv.Test(t, feature.Feature())
}

// TestDropRulesBlockNonMatchingEvent verifies that a CONTACT_SUPPORT health event
// WITHOUT the MANUAL_SUPPORT_REQUEST errorCode is dropped by the CEL drop rule.
// The node should still be quarantined (fault-quarantine is upstream of drop rules),
// but no RebootNode CR should be created.
func TestDropRulesBlockNonMatchingEvent(t *testing.T) {
	feature := features.New("TestDropRulesBlockNonMatchingEvent").
		WithLabel("suite", "drop-rules")

	var testCtx *helpers.RemediationTestContext

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		var newCtx context.Context
		newCtx, testCtx = helpers.SetupFaultRemediationTest(ctx, t, c, "")
		return newCtx
	})

	feature.Assess("CONTACT_SUPPORT without allowed errorCode is dropped", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		// Send a CONTACT_SUPPORT event with a different errorCode.
		// This should be dropped by the drop rule because it does NOT have
		// MANUAL_SUPPORT_REQUEST in errorCode.
		t.Log("Sending CONTACT_SUPPORT event with NODE_TEST_CONDITION_NOT_READY errorCode")
		event := helpers.NewHealthEvent(testCtx.NodeName).
			WithAgent("kubernetes-object-monitor").
			WithCheckName("node-test-condition").
			WithComponentClass("Node").
			WithMessage("Node test condition is not ready").
			WithRecommendedAction(5). // CONTACT_SUPPORT
			WithErrorCode("NODE_TEST_CONDITION_NOT_READY").
			WithEntity("v1/Node", testCtx.NodeName)
		helpers.SendHealthEvent(ctx, t, event)

		t.Log("Waiting for node to be cordoned (quarantined)")
		require.Eventually(t, func() bool {
			node, err := helpers.GetNodeByName(ctx, client, testCtx.NodeName)
			if err != nil {
				return false
			}
			return node.Spec.Unschedulable
		}, helpers.EventuallyWaitTimeout, helpers.WaitInterval)
		t.Log("Node cordoned — quarantine works (upstream of drop rules)")

		// Wait for the event to flow through the pipeline and give FR time to
		// process (or drop) it. The drop happens after quarantine + drain.
		t.Log("Waiting to confirm no RebootNode CR is created (event should be dropped)")
		time.Sleep(30 * time.Second)
		helpers.WaitForNoCR(ctx, t, client, testCtx.NodeName, helpers.RebootNodeGVK)
		t.Log("Confirmed: no RebootNode CR created — drop rule worked")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		return helpers.TeardownFaultRemediation(ctx, t, c)
	})

	testEnv.Test(t, feature.Feature())
}
