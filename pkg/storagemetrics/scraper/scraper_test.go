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

package scraper

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/storage"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	v1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

type fakeClient struct {
	mu      sync.Mutex
	results map[string]*storage.MetricsBatch
	errs    map[string]error
}

func (f *fakeClient) GetMetrics(_ context.Context, node *corev1.Node) (*storage.MetricsBatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.errs[node.Name]; ok {
		return nil, err
	}
	return f.results[node.Name], nil
}

func nodeListerFromNodes(t *testing.T, nodes ...*corev1.Node) v1listers.NodeLister {
	t.Helper()
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, n := range nodes {
		require.NoError(t, indexer.Add(runtime.Object(n)))
	}
	return v1listers.NewNodeLister(indexer)
}

func TestScrape_MergesNodes(t *testing.T) {
	now := time.Unix(1000, 0)
	nodeA := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	nodeB := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}}
	fc := &fakeClient{results: map[string]*storage.MetricsBatch{
		"a": {PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{
			{Namespace: "ns", Name: "p1"}: {Node: "a", Timestamp: now, CapacityBytes: 100, HasCapacity: true},
		}},
		"b": {PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{
			{Namespace: "ns", Name: "p2"}: {Node: "b", Timestamp: now, CapacityBytes: 200, HasCapacity: true},
		}},
	}}

	s := NewScraper(nodeListerFromNodes(t, nodeA, nodeB), fc, time.Second, nil)
	got := s.Scrape(context.Background())
	require.Len(t, got.PVCs, 2)
}

func TestScrape_PreferFreshestForRWXPVC(t *testing.T) {
	older := time.Unix(1000, 0)
	newer := time.Unix(2000, 0)
	nodeA := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	nodeB := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}}
	fc := &fakeClient{results: map[string]*storage.MetricsBatch{
		"a": {PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{
			{Namespace: "ns", Name: "shared"}: {Node: "a", Timestamp: older, CapacityBytes: 100, HasCapacity: true},
		}},
		"b": {PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{
			{Namespace: "ns", Name: "shared"}: {Node: "b", Timestamp: newer, CapacityBytes: 200, HasCapacity: true},
		}},
	}}
	s := NewScraper(nodeListerFromNodes(t, nodeA, nodeB), fc, time.Second, nil)
	got := s.Scrape(context.Background())
	require.Equal(t, "b", got.PVCs[apitypes.NamespacedName{Namespace: "ns", Name: "shared"}].Node)
}

func TestScrape_NodeErrorIsTolerated(t *testing.T) {
	nodeA := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "a"}}
	nodeB := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "b"}}
	fc := &fakeClient{
		results: map[string]*storage.MetricsBatch{
			"b": {PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{
				{Namespace: "ns", Name: "p1"}: {Node: "b", Timestamp: time.Unix(1, 0), CapacityBytes: 1, HasCapacity: true},
			}},
		},
		errs: map[string]error{"a": errors.New("boom")},
	}
	s := NewScraper(nodeListerFromNodes(t, nodeA, nodeB), fc, time.Second, nil)
	got := s.Scrape(context.Background())
	require.Len(t, got.PVCs, 1)
}
