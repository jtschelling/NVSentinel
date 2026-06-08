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

// Package managedtest holds test helpers shared by the consumers of
// commons/pkg/managed: the cluster-scope health monitors and the managed
// package itself. Lives in a separate subpackage so production binaries don't
// pull in the test-only dependency on client-go's fake clientset.
package managedtest

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	listersv1 "k8s.io/client-go/listers/core/v1"
)

// NodeListerWith returns an informer-backed NodeLister pre-populated with the
// given Nodes. Matches the production path: monitors and the ERR reconciler
// consume an informer-backed lister, so tests get the same lookup semantics
// (including the IsNotFound fail-open behaviour of IsNodeOptedOut).
func NodeListerWith(t *testing.T, nodes ...*corev1.Node) listersv1.NodeLister {
	t.Helper()

	factory := informers.NewSharedInformerFactory(fake.NewSimpleClientset(), 0)
	informer := factory.Core().V1().Nodes().Informer()

	for _, n := range nodes {
		require.NoError(t, informer.GetStore().Add(n))
	}

	return factory.Core().V1().Nodes().Lister()
}
