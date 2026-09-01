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

// Package v1 contains the MaintenanceRequest types, served at
// nvsentinel.dgxc.nvidia.com/v1. This is a different group from
// nvsentinel.nvidia.com/v1alpha1 in api/v1alpha1, which serves ValidationRequest.
//
// The types here wrap proto-generated messages, so they are kept out of the
// package controller-gen scans: it cannot build schemas from proto structs.
// The CRD is generated from data-models/protobufs/maintenance_request.proto by
// protoc-gen-crd instead. See ADR-051.
package v1

import (
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	protos "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

const (
	MaintenanceRequestAPIGroup = "nvsentinel.dgxc.nvidia.com"
	// MaintenanceRequestVersion is v1 because protoc-gen-crd hardcodes that
	// version. The API is still pre-stable; ADR-051 governs its lifecycle.
	MaintenanceRequestVersion = "v1"
	MaintenanceRequestKind    = "MaintenanceRequest"
)

var (
	GroupVersion = schema.GroupVersion{
		Group:   MaintenanceRequestAPIGroup,
		Version: MaintenanceRequestVersion,
	}

	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme registers the nvsentinel.dgxc.nvidia.com/v1 types. Callers
	// that also need ValidationRequest must register api/v1alpha1 as well.
	AddToScheme = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &MaintenanceRequest{}, &MaintenanceRequestList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)

	return nil
}

// MaintenanceRequest signals that maintenance is incoming for a node, so
// NVSentinel prepares it (cordon, drain) beforehand. Deleting the object clears
// the fault it raised. See ADR-051.
//
// Spec and Status are owned by the .proto file; this wrapper only adds the
// Kubernetes machinery. They are pointers because the proto types embed a
// sync.Mutex, which would trip go vet's copylocks on a value copy.
//
// +kubebuilder:object:generate=false
type MaintenanceRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   *protos.MaintenanceRequestSpec   `json:"spec,omitempty"`
	Status *protos.MaintenanceRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:object:generate=false
type MaintenanceRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenanceRequest `json:"items"`
}

func (m *MaintenanceRequest) DeepCopyObject() runtime.Object {
	if m == nil {
		return nil
	}

	out := new(MaintenanceRequest)
	m.DeepCopyInto(out)

	return out
}

func (m *MaintenanceRequest) DeepCopy() *MaintenanceRequest {
	if m == nil {
		return nil
	}

	return m.DeepCopyObject().(*MaintenanceRequest)
}

// DeepCopyInto clones Spec and Status with proto.Clone. A failed type assertion
// would mean proto runtime corruption, so panicking is correct here.
func (m *MaintenanceRequest) DeepCopyInto(out *MaintenanceRequest) {
	out.TypeMeta = m.TypeMeta
	m.ObjectMeta.DeepCopyInto(&out.ObjectMeta)

	if m.Spec != nil {
		out.Spec = proto.Clone(m.Spec).(*protos.MaintenanceRequestSpec)
	}

	if m.Status != nil {
		out.Status = proto.Clone(m.Status).(*protos.MaintenanceRequestStatus)
	}
}

func (l *MaintenanceRequestList) DeepCopyObject() runtime.Object {
	if l == nil {
		return nil
	}

	out := new(MaintenanceRequestList)
	l.DeepCopyInto(out)

	return out
}

func (l *MaintenanceRequestList) DeepCopy() *MaintenanceRequestList {
	if l == nil {
		return nil
	}

	return l.DeepCopyObject().(*MaintenanceRequestList)
}

func (l *MaintenanceRequestList) DeepCopyInto(out *MaintenanceRequestList) {
	out.TypeMeta = l.TypeMeta
	l.ListMeta.DeepCopyInto(&out.ListMeta)

	if l.Items != nil {
		out.Items = make([]MaintenanceRequest, len(l.Items))
		for i := range l.Items {
			l.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}
