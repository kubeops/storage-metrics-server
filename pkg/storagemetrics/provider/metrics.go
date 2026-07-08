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
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/storage"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Metric names exposed under custom.metrics.k8s.io/v1beta2 for the
// PersistentVolumeClaim group resource. These names follow the
// kubelet_volume_stats_* convention with the kubelet_volume_ prefix
// stripped, so dashboards and HPAs can reuse familiar terminology.
const (
	MetricVolumeCapacityBytes        = "volume_capacity_bytes"
	MetricVolumeAvailableBytes       = "volume_available_bytes"
	MetricVolumeUsedBytes            = "volume_used_bytes"
	MetricVolumeUsedPercentage       = "volume_used_percentage"
	MetricVolumeInodes               = "volume_inodes"
	MetricVolumeInodesFree           = "volume_inodes_free"
	MetricVolumeInodesUsed           = "volume_inodes_used"
	MetricVolumeInodesUsedPercentage = "volume_inodes_used_percentage"
)

// AllMetrics enumerates every PVC metric this provider can serve.
var AllMetrics = []string{
	MetricVolumeCapacityBytes,
	MetricVolumeAvailableBytes,
	MetricVolumeUsedBytes,
	MetricVolumeUsedPercentage,
	MetricVolumeInodes,
	MetricVolumeInodesFree,
	MetricVolumeInodesUsed,
	MetricVolumeInodesUsedPercentage,
}

// metricFromPoint extracts the named metric from a cached PVC observation.
//
// Bytes and inode counts are returned as decimal Quantities. Percentages are
// returned in milli units (1000m == 100%) so the standard HPA averageValue
// path keeps integer precision.
func metricFromPoint(metric string, p storage.PVCMetricsPoint) (resource.Quantity, bool) {
	switch metric {
	case MetricVolumeCapacityBytes:
		if !p.HasCapacity {
			return resource.Quantity{}, false
		}
		return *resource.NewQuantity(int64(p.CapacityBytes), resource.BinarySI), true //nolint:gosec
	case MetricVolumeAvailableBytes:
		if !p.HasCapacity {
			return resource.Quantity{}, false
		}
		return *resource.NewQuantity(int64(p.AvailableBytes), resource.BinarySI), true //nolint:gosec
	case MetricVolumeUsedBytes:
		return *resource.NewQuantity(int64(p.UsedBytes), resource.BinarySI), true //nolint:gosec
	case MetricVolumeUsedPercentage:
		if !p.HasCapacity || p.CapacityBytes == 0 {
			return resource.Quantity{}, false
		}
		// milli units: 1000m == 100%
		milli := int64(float64(p.UsedBytes) * 100000.0 / float64(p.CapacityBytes))
		return *resource.NewMilliQuantity(milli, resource.DecimalSI), true
	case MetricVolumeInodes:
		if !p.HasInodes {
			return resource.Quantity{}, false
		}
		return *resource.NewQuantity(int64(p.Inodes), resource.DecimalSI), true //nolint:gosec
	case MetricVolumeInodesFree:
		if !p.HasInodes {
			return resource.Quantity{}, false
		}
		return *resource.NewQuantity(int64(p.InodesFree), resource.DecimalSI), true //nolint:gosec
	case MetricVolumeInodesUsed:
		if !p.HasInodes {
			return resource.Quantity{}, false
		}
		return *resource.NewQuantity(int64(p.InodesUsed), resource.DecimalSI), true //nolint:gosec
	case MetricVolumeInodesUsedPercentage:
		if !p.HasInodes || p.Inodes == 0 {
			return resource.Quantity{}, false
		}
		milli := int64(float64(p.InodesUsed) * 100000.0 / float64(p.Inodes))
		return *resource.NewMilliQuantity(milli, resource.DecimalSI), true
	}
	return resource.Quantity{}, false
}
