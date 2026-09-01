/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	v1 "github.com/nvidia/nvsentinel/lifecycle-manager/api/v1"
	"github.com/nvidia/nvsentinel/lifecycle-manager/api/v1alpha1"
)

func newMaintenanceRequest(startTime time.Time) *v1.MaintenanceRequest {
	return &v1.MaintenanceRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "csp-maintenance-node-0"},
		Spec: &protos.MaintenanceRequestSpec{
			StartTime: timestamppb.New(startTime),
			HealthEvent: &protos.HealthEvent{
				Agent:                   "external-system",
				CheckName:               "csp-scheduled-maintenance",
				NodeName:                "node-0",
				IsHealthy:               false,
				RecommendedAction:       protos.RecommendedAction_CUSTOM,
				CustomRecommendedAction: "external-remediation",
			},
		},
	}
}

// The API server rejects the {"seconds":N,"nanos":M} form with 422.
func TestMarshalJSONEmitsRFC3339Timestamps(t *testing.T) {
	startTime := time.Date(2026, 5, 13, 3, 0, 0, 0, time.UTC)

	data, err := json.Marshal(newMaintenanceRequest(startTime))
	require.NoError(t, err)

	encoded := string(data)
	assert.Contains(t, encoded, `"startTime":"2026-05-13T03:00:00Z"`)
	assert.NotContains(t, encoded, `"seconds"`, "proto Timestamp leaked its reflection form")
	assert.NotContains(t, encoded, `"nanos"`, "proto Timestamp leaked its reflection form")
}

func TestJSONRoundTripPreservesSpec(t *testing.T) {
	startTime := time.Date(2026, 5, 13, 3, 0, 0, 0, time.UTC)

	data, err := json.Marshal(newMaintenanceRequest(startTime))
	require.NoError(t, err)

	var decoded v1.MaintenanceRequest
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "csp-maintenance-node-0", decoded.Name)
	assert.Equal(t, startTime, decoded.Spec.GetStartTime().AsTime())
	assert.Equal(t, "node-0", decoded.Spec.GetHealthEvent().GetNodeName())
	assert.Equal(t, protos.RecommendedAction_CUSTOM, decoded.Spec.GetHealthEvent().GetRecommendedAction())
	assert.Equal(t, "external-remediation", decoded.Spec.GetHealthEvent().GetCustomRecommendedAction())
}

// Callers must be able to tell "unset" from "set to zero".
func TestUnmarshalJSONTreatsNullAsAbsent(t *testing.T) {
	for name, payload := range map[string]string{
		"omitted": `{"metadata":{"name":"mr-0"}}`,
		"null":    `{"metadata":{"name":"mr-0"},"spec":null,"status":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			var decoded v1.MaintenanceRequest
			require.NoError(t, json.Unmarshal([]byte(payload), &decoded))
			assert.Nil(t, decoded.Spec)
			assert.Nil(t, decoded.Status)
		})
	}
}

func TestListJSONRoundTrip(t *testing.T) {
	list := &v1.MaintenanceRequestList{
		Items: []v1.MaintenanceRequest{*newMaintenanceRequest(time.Now().UTC())},
	}

	data, err := json.Marshal(list)
	require.NoError(t, err)
	assert.False(t, strings.Contains(string(data), `"seconds"`))

	var decoded v1.MaintenanceRequestList
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Items, 1)
	assert.Equal(t, "node-0", decoded.Items[0].Spec.GetHealthEvent().GetNodeName())
}

// The two API groups must share one scheme.
func TestSchemeRegistration(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	gvks, _, err := scheme.ObjectKinds(&v1.MaintenanceRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, gvks)
	assert.Equal(t, "nvsentinel.dgxc.nvidia.com", gvks[0].Group)
	assert.Equal(t, "v1", gvks[0].Version)
	assert.Equal(t, "MaintenanceRequest", gvks[0].Kind)

	listGVKs, _, err := scheme.ObjectKinds(&v1.MaintenanceRequestList{})
	require.NoError(t, err)
	require.NotEmpty(t, listGVKs)
	assert.Equal(t, "MaintenanceRequestList", listGVKs[0].Kind)
}

// controller-runtime hands cached objects to reconcilers, so this must not alias.
func TestDeepCopyIsDeep(t *testing.T) {
	original := newMaintenanceRequest(time.Now().UTC())

	clone := original.DeepCopy()
	clone.Spec.HealthEvent.NodeName = "mutated"
	clone.Name = "mutated"

	assert.Equal(t, "node-0", original.Spec.GetHealthEvent().GetNodeName())
	assert.Equal(t, "csp-maintenance-node-0", original.Name)
}

func TestDeepCopyHandlesNilFields(t *testing.T) {
	assert.Nil(t, (*v1.MaintenanceRequest)(nil).DeepCopy())
	assert.Nil(t, (*v1.MaintenanceRequestList)(nil).DeepCopy())

	bare := &v1.MaintenanceRequest{}
	clone := bare.DeepCopy()
	assert.Nil(t, clone.Spec)
	assert.Nil(t, clone.Status)
}
