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

// Package provider implements provider.CustomMetricsProvider over a PVC
// metrics cache scraped from kubelet's /stats/summary.
package provider

import (
	"context"
	"fmt"

	"kubeops.dev/storage-metrics-apiserver/pkg/provider"
	"kubeops.dev/storage-metrics-apiserver/pkg/storagemetrics/storage"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	v1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/metrics/pkg/apis/custom_metrics"
)

// pvcGroupResource is the only resource we serve metrics on. We hard-code
// the v1 PVC group/resource because /stats/summary keys volume entries by
// PVC reference, which is always v1.
var pvcGroupResource = schema.GroupResource{Group: "", Resource: "persistentvolumeclaims"}

// PVCLister is the subset of v1listers.PersistentVolumeClaimLister this
// provider needs. The interface is broken out so tests can supply a fake.
type PVCLister interface {
	// PVCs returns a lister for the given namespace.
	PVCs(namespace string) PVCNamespaceLister
}

// PVCNamespaceLister is the subset we need from a per-namespace PVC lister.
type PVCNamespaceLister interface {
	List(selector labels.Selector) ([]*corev1.PersistentVolumeClaim, error)
}

type informerPVCLister struct {
	lister v1listers.PersistentVolumeClaimLister
}

func (l *informerPVCLister) PVCs(namespace string) PVCNamespaceLister {
	return l.lister.PersistentVolumeClaims(namespace)
}

// FromInformerLister wraps a standard PVC informer lister to satisfy the
// PVCLister interface used by this provider.
func FromInformerLister(lister v1listers.PersistentVolumeClaimLister) PVCLister {
	return &informerPVCLister{lister: lister}
}

// Provider serves the volume_* custom metrics under
// custom.metrics.k8s.io/v1beta2 for PersistentVolumeClaim resources.
type Provider struct {
	store     *storage.Storage
	pvcLister PVCLister
}

var _ provider.CustomMetricsProvider = (*Provider)(nil)

// NewProvider builds a provider that reads from the supplied cache and
// uses the PVC lister to resolve label selectors against the live cluster.
func NewProvider(store *storage.Storage, pvcLister PVCLister) *Provider {
	return &Provider{store: store, pvcLister: pvcLister}
}

// ListAllMetrics enumerates every (resource, metric) pair this provider
// can answer. The custom metrics installer uses this to populate the
// /apis/custom.metrics.k8s.io/v1beta2 discovery doc.
func (p *Provider) ListAllMetrics() []provider.CustomMetricInfo {
	out := make([]provider.CustomMetricInfo, 0, len(AllMetrics))
	for _, m := range AllMetrics {
		out = append(out, provider.CustomMetricInfo{
			GroupResource: pvcGroupResource,
			Metric:        m,
			Namespaced:    true,
		})
	}
	return out
}

// GetMetricByName returns the latest cached value for one PVC.
func (p *Provider) GetMetricByName(_ context.Context, name apitypes.NamespacedName, info provider.CustomMetricInfo, _ labels.Selector) (*custom_metrics.MetricValue, error) {
	if !isPVC(info) {
		return nil, provider.NewMetricNotFoundError(info.GroupResource, info.Metric)
	}
	point, ok := p.store.Get(name)
	if !ok {
		return nil, provider.NewMetricNotFoundForError(info.GroupResource, info.Metric, name.Name)
	}
	mv, ok := buildMetricValue(point, name, info.Metric)
	if !ok {
		return nil, provider.NewMetricNotFoundForError(info.GroupResource, info.Metric, name.Name)
	}
	return mv, nil
}

// GetMetricBySelector returns metrics for every PVC in the namespace that
// matches the given label selector. We resolve the selector against the
// live PVC informer so the response shape mirrors prometheus-adapter and
// the HPA controller's expectations.
func (p *Provider) GetMetricBySelector(_ context.Context, namespace string, selector labels.Selector, info provider.CustomMetricInfo, _ labels.Selector) (*custom_metrics.MetricValueList, error) {
	if !isPVC(info) {
		return nil, provider.NewMetricNotFoundError(info.GroupResource, info.Metric)
	}
	if p.pvcLister == nil {
		return nil, fmt.Errorf("PVC lister not configured")
	}
	pvcs, err := p.pvcLister.PVCs(namespace).List(selector)
	if err != nil {
		return nil, err
	}

	items := make([]custom_metrics.MetricValue, 0, len(pvcs))
	for _, pvc := range pvcs {
		key := apitypes.NamespacedName{Namespace: namespace, Name: pvc.Name}
		point, ok := p.store.Get(key)
		if !ok {
			// PVC exists in the cluster but kubelet hasn't reported stats
			// for it yet (e.g. unbound, unmounted). Skip — returning a
			// 404 for the whole list breaks HPA queries that span many
			// PVCs where only some are mounted.
			continue
		}
		mv, ok := buildMetricValue(point, key, info.Metric)
		if !ok {
			continue
		}
		items = append(items, *mv)
	}
	return &custom_metrics.MetricValueList{Items: items}, nil
}

func isPVC(info provider.CustomMetricInfo) bool {
	return info.GroupResource == pvcGroupResource
}

func buildMetricValue(point storage.PVCMetricsPoint, name apitypes.NamespacedName, metric string) (*custom_metrics.MetricValue, bool) {
	q, ok := metricFromPoint(metric, point)
	if !ok {
		return nil, false
	}
	return &custom_metrics.MetricValue{
		DescribedObject: custom_metrics.ObjectReference{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
			Name:       name.Name,
			Namespace:  name.Namespace,
		},
		Metric:    custom_metrics.MetricIdentifier{Name: metric},
		Timestamp: metav1.NewTime(point.Timestamp),
		Value:     q,
	}, true
}
