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

// Spec and Status are proto messages, so they must be marshalled with protojson
// rather than encoding/json. encoding/json reflects over google.protobuf.Timestamp
// and emits {"seconds":N,"nanos":M}, which the CRD schema rejects with HTTP 422;
// protojson emits the RFC3339 string the schema declares. TypeMeta and ObjectMeta
// are ordinary k8s types and still go through encoding/json.

package v1

import (
	"bytes"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

// DiscardUnknown matches apimachinery's tolerance of server-side schema additions.
var protojsonUnmarshalOpts = protojson.UnmarshalOptions{DiscardUnknown: true}

// jsonEnvelope holds Spec and Status as opaque JSON so protojson can produce or
// consume them independently of the surrounding object.
type jsonEnvelope struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              json.RawMessage `json:"spec,omitempty"`
	Status            json.RawMessage `json:"status,omitempty"`
}

func (m *MaintenanceRequest) MarshalJSON() ([]byte, error) {
	out := jsonEnvelope{TypeMeta: m.TypeMeta, ObjectMeta: m.ObjectMeta}

	if m.Spec != nil {
		b, err := protojson.Marshal(m.Spec)
		if err != nil {
			return nil, fmt.Errorf("marshal MaintenanceRequest.spec via protojson: %w", err)
		}

		out.Spec = b
	}

	if m.Status != nil {
		b, err := protojson.Marshal(m.Status)
		if err != nil {
			return nil, fmt.Errorf("marshal MaintenanceRequest.status via protojson: %w", err)
		}

		out.Status = b
	}

	return json.Marshal(&out)
}

func (m *MaintenanceRequest) UnmarshalJSON(data []byte) error {
	var in jsonEnvelope
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("unmarshal MaintenanceRequest envelope: %w", err)
	}

	m.TypeMeta = in.TypeMeta
	m.ObjectMeta = in.ObjectMeta
	m.Spec = nil
	m.Status = nil

	if isJSONPresent(in.Spec) {
		spec := &protos.MaintenanceRequestSpec{}
		if err := protojsonUnmarshalOpts.Unmarshal(in.Spec, spec); err != nil {
			return fmt.Errorf("unmarshal MaintenanceRequest.spec via protojson: %w", err)
		}

		m.Spec = spec
	}

	if isJSONPresent(in.Status) {
		status := &protos.MaintenanceRequestStatus{}
		if err := protojsonUnmarshalOpts.Unmarshal(in.Status, status); err != nil {
			return fmt.Errorf("unmarshal MaintenanceRequest.status via protojson: %w", err)
		}

		m.Status = status
	}

	return nil
}

type listJSONEnvelope struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenanceRequest `json:"items"`
}

func (l *MaintenanceRequestList) MarshalJSON() ([]byte, error) {
	return json.Marshal(&listJSONEnvelope{
		TypeMeta: l.TypeMeta,
		ListMeta: l.ListMeta,
		Items:    l.Items,
	})
}

func (l *MaintenanceRequestList) UnmarshalJSON(data []byte) error {
	var in listJSONEnvelope
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("unmarshal MaintenanceRequestList: %w", err)
	}

	l.TypeMeta = in.TypeMeta
	l.ListMeta = in.ListMeta
	l.Items = in.Items

	return nil
}

// isJSONPresent treats empty, missing and explicit null alike as absent.
func isJSONPresent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	return !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

var (
	_ proto.Message = (*protos.MaintenanceRequestSpec)(nil)
	_ proto.Message = (*protos.MaintenanceRequestStatus)(nil)
)
