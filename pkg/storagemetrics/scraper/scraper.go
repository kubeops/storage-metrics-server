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

// Package scraper periodically asks every kubelet in the cluster for the
// current PVC volume stats and merges the responses into a single batch.
package scraper

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"kubeops.dev/storage-metrics-apiserver/pkg/storagemetrics/scraper/client"
	"kubeops.dev/storage-metrics-apiserver/pkg/storagemetrics/storage"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	apitypes "k8s.io/apimachinery/pkg/types"
	v1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

// Scraper merges per-node /stats/summary responses into a single PVC batch.
type Scraper interface {
	Scrape(ctx context.Context) *storage.MetricsBatch
}

const (
	maxDelayMs       = 4 * 1000
	delayPerSourceMs = 8
)

type scraper struct {
	nodeLister    v1listers.NodeLister
	kubeletClient client.KubeletVolumeMetricsGetter
	scrapeTimeout time.Duration
	labelSelector labels.Selector
}

// NewScraper builds a Scraper that lists nodes via the supplied lister and
// fans out a /stats/summary fetch to each one in a goroutine, with the
// staggering pattern from metrics-server to avoid network spikes.
func NewScraper(nodeLister v1listers.NodeLister, kc client.KubeletVolumeMetricsGetter, scrapeTimeout time.Duration, labelRequirement []labels.Requirement) Scraper {
	sel := labels.Everything()
	if labelRequirement != nil {
		sel = sel.Add(labelRequirement...)
	}
	return &scraper{
		nodeLister:    nodeLister,
		kubeletClient: kc,
		scrapeTimeout: scrapeTimeout,
		labelSelector: sel,
	}
}

func (c *scraper) Scrape(baseCtx context.Context) *storage.MetricsBatch {
	nodes, err := c.nodeLister.List(c.labelSelector)
	if err != nil {
		klog.ErrorS(err, "Failed to list nodes")
	}
	klog.V(2).InfoS("Scraping kubelets", "nodeCount", len(nodes))

	resCh := make(chan *storage.MetricsBatch, len(nodes))
	defer close(resCh)

	startTime := time.Now()
	delayMs := min(delayPerSourceMs*len(nodes), maxDelayMs)

	for _, node := range nodes {
		go func(node *corev1.Node) {
			if delayMs > 0 {
				time.Sleep(time.Duration(rand.Intn(delayMs)) * time.Millisecond) //nolint:gosec
			}
			ctx, cancel := context.WithTimeout(baseCtx, c.scrapeTimeout)
			defer cancel()
			m, err := c.kubeletClient.GetMetrics(ctx, node)
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					klog.ErrorS(err, "Kubelet scrape timed out", "node", klog.KObj(node), "timeout", c.scrapeTimeout)
				} else {
					klog.ErrorS(err, "Failed to scrape kubelet", "node", klog.KObj(node))
				}
			}
			resCh <- m
		}(node)
	}

	merged := &storage.MetricsBatch{PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{}}
	for range nodes {
		batch := <-resCh
		if batch == nil {
			continue
		}
		for key, point := range batch.PVCs {
			if existing, ok := merged.PVCs[key]; ok {
				// Same PVC reported by multiple nodes (RWX). Keep the freshest sample.
				if existing.Timestamp.After(point.Timestamp) {
					continue
				}
			}
			merged.PVCs[key] = point
		}
	}
	klog.V(2).InfoS("Scrape finished", "duration", time.Since(startTime), "pvcCount", len(merged.PVCs))
	return merged
}
