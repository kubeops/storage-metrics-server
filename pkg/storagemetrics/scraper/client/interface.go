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

// Package client defines the interface used by the storage-metrics scraper
// to fetch volume filesystem stats from a single kubelet, and provides an
// HTTP-based implementation against /stats/summary.
package client

import (
	"context"

	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/storage"

	corev1 "k8s.io/api/core/v1"
)

// KubeletVolumeMetricsGetter knows how to fetch PVC volume filesystem
// stats from a single Kubelet.
type KubeletVolumeMetricsGetter interface {
	// GetMetrics fetches volume metrics from the given Kubelet and returns
	// a per-PVC batch keyed by {namespace, name}.
	GetMetrics(ctx context.Context, node *corev1.Node) (*storage.MetricsBatch, error)
}
