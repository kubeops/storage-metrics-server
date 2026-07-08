/*
Copyright 2026 AppsCode Inc.

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

package provider

import (
	"context"
	"testing"
	"time"

	provider2 "kubeops.dev/storage-metrics-server/pkg/provider"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/storage"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
)

type fakePVCLister struct {
	pvcs map[string][]*corev1.PersistentVolumeClaim
}

func (f *fakePVCLister) PVCs(namespace string) PVCNamespaceLister {
	return &fakePVCNSLister{items: f.pvcs[namespace]}
}

type fakePVCNSLister struct {
	items []*corev1.PersistentVolumeClaim
}

func (f *fakePVCNSLister) List(sel labels.Selector) ([]*corev1.PersistentVolumeClaim, error) {
	out := []*corev1.PersistentVolumeClaim{}
	for _, p := range f.items {
		if sel == nil || sel.Matches(labels.Set(p.Labels)) {
			out = append(out, p)
		}
	}
	return out, nil
}

func mkPVC(ns, name string, lbls map[string]string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: lbls}}
}

func newPopulatedStore(t *testing.T) *storage.Storage {
	t.Helper()
	store := storage.NewStorage()
	store.Store(&storage.MetricsBatch{
		PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{
			{Namespace: "ns1", Name: "data"}: {
				Timestamp:      time.Unix(2000, 0),
				CapacityBytes:  1024,
				AvailableBytes: 256,
				UsedBytes:      768,
				Inodes:         100,
				InodesFree:     90,
				InodesUsed:     10,
				HasCapacity:    true,
				HasInodes:      true,
			},
		},
	})
	return store
}

func TestProvider_GetMetricByName(t *testing.T) {
	p := NewProvider(newPopulatedStore(t), nil)

	info := provider2.CustomMetricInfo{
		GroupResource: pvcGroupResource,
		Metric:        MetricVolumeUsedBytes,
		Namespaced:    true,
	}
	got, err := p.GetMetricByName(context.Background(), apitypes.NamespacedName{Namespace: "ns1", Name: "data"}, info, labels.Everything())
	require.NoError(t, err)
	require.Equal(t, int64(768), got.Value.Value())
	require.Equal(t, "PersistentVolumeClaim", got.DescribedObject.Kind)
}

func TestProvider_GetMetricByName_UnknownPVC(t *testing.T) {
	p := NewProvider(newPopulatedStore(t), nil)
	info := provider2.CustomMetricInfo{GroupResource: pvcGroupResource, Metric: MetricVolumeUsedBytes, Namespaced: true}
	_, err := p.GetMetricByName(context.Background(), apitypes.NamespacedName{Namespace: "ns1", Name: "missing"}, info, labels.Everything())
	require.Error(t, err)
	require.True(t, apierr.IsNotFound(err))
}

func TestProvider_GetMetricByName_WrongResource(t *testing.T) {
	p := NewProvider(newPopulatedStore(t), nil)
	info := provider2.CustomMetricInfo{GroupResource: schema.GroupResource{Resource: "pods"}, Metric: MetricVolumeUsedBytes, Namespaced: true}
	_, err := p.GetMetricByName(context.Background(), apitypes.NamespacedName{Namespace: "ns1", Name: "data"}, info, labels.Everything())
	require.Error(t, err)
}

func TestProvider_GetMetricByName_PercentageInMilli(t *testing.T) {
	p := NewProvider(newPopulatedStore(t), nil)
	info := provider2.CustomMetricInfo{GroupResource: pvcGroupResource, Metric: MetricVolumeUsedPercentage, Namespaced: true}
	got, err := p.GetMetricByName(context.Background(), apitypes.NamespacedName{Namespace: "ns1", Name: "data"}, info, labels.Everything())
	require.NoError(t, err)
	// 768/1024 = 75% -> 75000 milli
	require.Equal(t, int64(75000), got.Value.MilliValue())
}

func TestProvider_GetMetricBySelector_SkipsUnscrapedPVCs(t *testing.T) {
	store := newPopulatedStore(t)
	lister := &fakePVCLister{pvcs: map[string][]*corev1.PersistentVolumeClaim{
		"ns1": {
			mkPVC("ns1", "data", map[string]string{"app": "x"}),
			mkPVC("ns1", "unmounted", map[string]string{"app": "x"}),
			mkPVC("ns1", "other", map[string]string{"app": "y"}),
		},
	}}
	p := NewProvider(store, lister)

	info := provider2.CustomMetricInfo{GroupResource: pvcGroupResource, Metric: MetricVolumeUsedBytes, Namespaced: true}
	sel, err := labels.Parse("app=x")
	require.NoError(t, err)

	list, err := p.GetMetricBySelector(context.Background(), "ns1", sel, info, labels.Everything())
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, "data", list.Items[0].DescribedObject.Name)
}

func TestProvider_ListAllMetrics(t *testing.T) {
	p := NewProvider(storage.NewStorage(), nil)
	all := p.ListAllMetrics()
	require.Len(t, all, len(AllMetrics))
	for _, m := range all {
		require.Equal(t, pvcGroupResource, m.GroupResource)
		require.True(t, m.Namespaced)
	}
}
