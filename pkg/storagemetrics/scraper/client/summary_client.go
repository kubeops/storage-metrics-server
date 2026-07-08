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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"kubeops.dev/storage-metrics-apiserver/pkg/storagemetrics/storage"

	corev1 "k8s.io/api/core/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	stats "k8s.io/kubelet/pkg/apis/stats/v1alpha1"
)

// summaryClient hits a kubelet's /stats/summary endpoint and converts the
// per-pod VolumeStats into a per-PVC MetricsBatch.
//
// We use /stats/summary (rather than the Prometheus /metrics endpoint) for
// two reasons:
//  1. /stats/summary works for any volume kubelet mounts as a filesystem
//     PVC — in-tree, external CSI, and migrated drivers — without depending
//     on the CSI driver implementing NodeGetVolumeStats.
//  2. The PVC reference is provided alongside each volume entry, so we
//     don't have to join on pod -> volume -> PVC ourselves.
type summaryClient struct {
	defaultPort       int
	useNodeStatusPort bool
	client            *http.Client
	scheme            string
	addrResolver      NodeAddressResolver
}

var _ KubeletVolumeMetricsGetter = (*summaryClient)(nil)

// NewForConfig builds a kubelet client that fetches /stats/summary using the
// supplied REST config (auth, TLS) and address resolution rules.
func NewForConfig(config *KubeletClientConfig) (*summaryClient, error) {
	transport, err := rest.TransportFor(&config.Client)
	if err != nil {
		return nil, fmt.Errorf("unable to construct transport: %w", err)
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   config.Client.Timeout,
	}
	return newClient(httpClient, NewPriorityNodeAddressResolver(config.AddressTypePriority),
		config.DefaultPort, config.Scheme, config.UseNodeStatusPort), nil
}

func newClient(c *http.Client, resolver NodeAddressResolver, defaultPort int, scheme string, useNodeStatusPort bool) *summaryClient {
	return &summaryClient{
		addrResolver:      resolver,
		defaultPort:       defaultPort,
		client:            c,
		scheme:            scheme,
		useNodeStatusPort: useNodeStatusPort,
	}
}

func (kc *summaryClient) GetMetrics(ctx context.Context, node *corev1.Node) (*storage.MetricsBatch, error) {
	port := kc.defaultPort
	nodeStatusPort := int(node.Status.DaemonEndpoints.KubeletEndpoint.Port)
	if kc.useNodeStatusPort && nodeStatusPort != 0 {
		port = nodeStatusPort
	}
	addr, err := kc.addrResolver.NodeAddress(node)
	if err != nil {
		return nil, err
	}
	u := url.URL{
		Scheme: kc.scheme,
		Host:   net.JoinHostPort(addr, strconv.Itoa(port)),
		Path:   "/stats/summary",
		// Only include volume stats — no per-container / network sub-trees.
		RawQuery: "only_cpu_and_memory=false",
	}
	return kc.getMetrics(ctx, u.String(), node.Name)
}

func (kc *summaryClient) getMetrics(ctx context.Context, u, nodeName string) (*storage.MetricsBatch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	requestTime := time.Now()
	resp, err := kc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("kubelet %q stats/summary request failed: %s", nodeName, resp.Status)
	}

	var summary stats.Summary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return nil, fmt.Errorf("decode kubelet stats summary for node %q: %w", nodeName, err)
	}
	return summaryToBatch(&summary, requestTime, nodeName), nil
}

// summaryToBatch flattens summary.Pods[].VolumeStats[] into a per-PVC map.
//
// One PVC may appear in many pods (RWX) and across nodes (RWX). We keep the
// freshest sample so the cache reflects current capacity even when the
// volume mounts get re-shuffled across pods.
func summaryToBatch(summary *stats.Summary, defaultTime time.Time, nodeName string) *storage.MetricsBatch {
	batch := &storage.MetricsBatch{PVCs: map[apitypes.NamespacedName]storage.PVCMetricsPoint{}}
	for i := range summary.Pods {
		pod := &summary.Pods[i]
		for j := range pod.VolumeStats {
			v := &pod.VolumeStats[j]
			if v.PVCRef == nil || v.PVCRef.Name == "" {
				// emptyDir / configMap / secret etc. — not a PVC.
				continue
			}
			ts := v.Time.Time
			if ts.IsZero() {
				ts = defaultTime
			}
			point := storage.PVCMetricsPoint{
				Node:      nodeName,
				Timestamp: ts,
			}
			if v.CapacityBytes != nil {
				point.CapacityBytes = *v.CapacityBytes
				point.HasCapacity = true
			}
			if v.AvailableBytes != nil {
				point.AvailableBytes = *v.AvailableBytes
			}
			if v.UsedBytes != nil {
				point.UsedBytes = *v.UsedBytes
			}
			if v.Inodes != nil {
				point.Inodes = *v.Inodes
				point.HasInodes = true
			}
			if v.InodesFree != nil {
				point.InodesFree = *v.InodesFree
			}
			if v.InodesUsed != nil {
				point.InodesUsed = *v.InodesUsed
			}
			if !point.HasCapacity && !point.HasInodes && point.UsedBytes == 0 {
				// Driver returned an entry but no usable numbers. Skip it
				// rather than caching a misleading zeroed-out point.
				klog.V(4).InfoS("Skipping PVC volume stat with no usable values",
					"pvc", klog.KRef(v.PVCRef.Namespace, v.PVCRef.Name), "node", nodeName)
				continue
			}
			key := apitypes.NamespacedName{Namespace: v.PVCRef.Namespace, Name: v.PVCRef.Name}
			if existing, ok := batch.PVCs[key]; ok && existing.Timestamp.After(point.Timestamp) {
				continue
			}
			batch.PVCs[key] = point
		}
	}
	return batch
}
