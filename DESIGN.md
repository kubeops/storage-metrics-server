# DESIGN.md

Design notes for the AppsCode **Kubernetes Storage Metrics Server** — the
storage-metrics addition layered on top of the
[`custom-metrics-apiserver`](https://github.com/kubernetes-sigs/custom-metrics-apiserver)
fork. This document explains *how* the server works and *why* it is built the
way it is. For an overview and usage, see [README.md](README.md); for testing,
[TEST.md](TEST.md); for a developer walkthrough, [DEV.md](DEV.md).

## Goal

Expose per-PVC filesystem capacity, usage, and inode metrics through the
**stable** `custom.metrics.k8s.io/v1beta2` API on the
`persistentvolumeclaims` resource, so that an HPA, VPA, or operator can
autoscale (or alert on) volume utilization the same way it reads CPU/memory
from `metrics-server`.

The metrics served are:

| Metric | Unit | Source field (kubelet FsStats) |
| --- | --- | --- |
| `volume_capacity_bytes` | bytes (BinarySI) | `CapacityBytes` |
| `volume_available_bytes` | bytes (BinarySI) | `AvailableBytes` |
| `volume_used_bytes` | bytes (BinarySI) | `UsedBytes` |
| `volume_used_percentage` | milli (1000m == 100%) | `UsedBytes / CapacityBytes` |
| `volume_inodes` | count (DecimalSI) | `Inodes` |
| `volume_inodes_free` | count | `InodesFree` |
| `volume_inodes_used` | count | `InodesUsed` |
| `volume_inodes_used_percentage` | milli (1000m == 100%) | `InodesUsed / Inodes` |

Metric names follow the kubelet `kubelet_volume_stats_*` convention with the
`kubelet_volume_` prefix stripped, so existing dashboards and mental models
carry over. These names are the **user contract** for HPAs; they are not
renamed without a coordinated migration.

## Data flow

```
                                        every --metric-resolution (default 60s)
                                        ┌───────────────────────────────────────┐
                                        ▼                                       │
  Node informer ──lister──►  scraper.Scrape(ctx)                               │
                                 │  fan out one goroutine per Node             │
                                 │  (staggered start to avoid spikes)          │
                                 ▼                                             │
                       client.GetMetrics(node)                                │
                       GET https://<nodeAddr>:<port>/stats/summary            │
                                 │                                            │
                                 ▼                                            │
                       summaryToBatch(): Pods[].VolumeStats[]  ──►  per-PVC   │
                       filter out non-PVC volumes (emptyDir, configMap, …)    │
                                 │                                            │
                                 ▼                                            │
                       merge batches across nodes (freshest sample wins)  ────┘
                                 │
                                 ▼
                       storage.Store(batch)   atomic map swap under RWMutex
                                 │
                                 ▼
  ── API request ──►  provider.GetMetricByName / GetMetricBySelector
                       reads storage snapshot + PVC informer (for selectors)
                                 │
                                 ▼
                       custom_metrics.MetricValue  (via metricFromPoint)
```

Two loops run concurrently and share only the `storage.Storage` cache:

1. **Scrape loop** (`manager.RunUntil`) — ticks on a `time.Ticker`, scrapes
   every kubelet, and overwrites the cache. It is pull-based and independent
   of API traffic.
2. **Serve path** (`provider.Provider`) — driven by incoming custom-metrics
   API requests, reads the latest cached snapshot. It never blocks on a
   scrape.

## Components

Each lives under `pkg/storagemetrics/`:

- **`options/`** — CLI flag definitions and validation.
  `KubeletClientOptions` (kubelet auth/TLS/address, ported from
  `metrics-server` so operators reuse familiar flags) and
  `StorageMetricsOptions` (`--metric-resolution`, floored at 10s).
- **`scraper/client/`** — the kubelet HTTP client. `summaryClient` hits
  `/stats/summary?only_cpu_and_memory=false` on one node, decodes the
  `stats.Summary`, and flattens `Pods[].VolumeStats[]` into a per-PVC batch
  (`summaryToBatch`). `address_resolver.go` picks a node address by a
  priority list.
- **`scraper/`** — fans a per-node goroutine out across the cluster, merges
  the per-node batches into one, and dedups PVCs seen from multiple nodes.
- **`storage/`** — an in-memory cache (`Storage`) holding the last scraped
  batch (`MetricsBatch` of `PVCMetricsPoint`), plus a `Ready()` flag.
- **`manager/`** — the tick loop wiring scraper → storage, with per-tick
  timeout and a `LastTickStart()` accessor for staleness checks.
- **`provider/`** — implements `provider.CustomMetricsProvider`. Converts a
  cached `PVCMetricsPoint` into a `custom_metrics.MetricValue`
  (`metricFromPoint`), and resolves label selectors against a live PVC
  informer lister.

`cmd/storage-metrics-server/main.go` wires it all together on top of the
upstream `basecmd.AdapterBase`.

## Key design decisions

### 1. Read `/stats/summary`, not CSI `NodeGetVolumeStats`

**Load-bearing.** The scraper reads each kubelet's `/stats/summary` endpoint
rather than calling the CSI driver's `NodeGetVolumeStats`. Kubelet already
collects filesystem stats for **every** volume it mounts as a filesystem —
in-tree drivers, external CSI, CSI-migrated in-tree volumes, and pre-existing
PVCs — regardless of whether the CSI driver implements the (optional)
`NodeGetVolumeStats` capability. Using `/stats/summary` is what gives the
server its "works on legacy and any-driver PVCs" property. Switching to
`NodeGetVolumeStats` would silently drop metrics for old in-tree volumes and
drivers that don't implement that RPC.

A second benefit: each `VolumeStats` entry carries its `PVCRef`
(namespace + name), so the server does not have to join pod → volume → PVC
itself. Volumes with no `PVCRef` (emptyDir, configMap, secret, …) are skipped.

### 2. In-memory gauge cache, no time-window aggregation

PVC filesystem stats are **gauges**, not counters — "how full is the volume
right now," not "bytes written since boot." So the cache keeps only the most
recent sample per PVC; there is no rate calculation or sliding window (unlike
CPU in `metrics-server`). `Storage.Store` **atomically swaps** the whole map
under a write lock, so a reader either sees the entire previous batch or the
entire new one — never a half-updated map. Memory is `O(PVCs in cluster)`,
one `PVCMetricsPoint` per claim.

### 3. Freshest-sample-wins for RWX volumes

A ReadWriteMany PVC can be mounted by many pods across many nodes, so the same
PVC key appears in multiple kubelets' summaries. Dedup happens **twice** and
both times the newer `Timestamp` wins:

- within one node's summary (`summaryToBatch`), and
- when merging across nodes (`scraper.Scrape`).

This keeps the cache reflecting the latest reading even as mounts reshuffle.

### 4. Staggered scrape fan-out

To avoid a synchronized network spike (and a kubelet thundering herd) on large
clusters, each per-node goroutine sleeps a random `[0, delayMs)` before its
request, where `delayMs = min(8 * nodeCount, 4000)` — the same pattern
`metrics-server` uses. A node scrape failure is logged and tolerated; that
node simply contributes nothing to the batch that tick.

### 5. Dedicated informer factory, independent of the apiserver

`main.go` builds a **separate** `SharedInformerFactory` for Node and PVC
objects rather than reusing `AdapterBase`'s (which is tied to the API server
lifecycle). The scrape loop waits only for its own Node/PVC caches to sync,
then starts ticking — it doesn't wait for the aggregated API server to finish
wiring handlers, so the cache warms as early as possible.

### 6. Percentages in milli units

`volume_used_percentage` and `volume_inodes_used_percentage` are returned as
`resource.Quantity` **milli** values (`1000m == 100%`). This keeps integer
precision through the HPA `averageValue`/`value` path, which works in
Quantities, instead of losing resolution to a rounded whole-percent integer.

### 7. Selector queries skip un-scraped PVCs

`GetMetricBySelector` lists matching PVCs from the informer, then looks each
up in the cache. A PVC that exists but has no cached sample yet (unbound,
unmounted, or not scraped since startup) is **skipped**, not errored. Returning
a 404 for the whole list would break HPA queries that span many PVCs where
only some are currently mounted. `GetMetricByName` (single PVC) *does* return
`MetricNotFound` when there is no sample, matching the single-object contract.

### 8. Missing-field guards

kubelet may omit fields (`CapacityBytes`, `Inodes`, …) for some drivers.
`PVCMetricsPoint` tracks `HasCapacity` / `HasInodes`, and `metricFromPoint`
returns "not found" for a metric whose backing field is absent (or whose
denominator is zero for a percentage), rather than serving a misleading `0`.
An entry with no usable numbers at all is dropped at scrape time.

## API surface & compatibility

- Served group/version: `custom.metrics.k8s.io/v1beta2` (the upstream library
  also registers `v1beta1`; both APIServices are installed by the manifests).
- The only group-resource served is the core-group `persistentvolumeclaims`
  (`{Group: "", Resource: "persistentvolumeclaims"}`), hard-coded because
  `/stats/summary` keys volumes by a v1 PVC reference.
- All metrics are namespaced.
- The `custom.metrics.k8s.io` wire format is a **stable Kubernetes API** — the
  server must not break it.

## Deployment shape

Runs as a single-replica aggregated API server Deployment
(`system-cluster-critical`, non-root, read-only root FS). It registers
`v1beta1`/`v1beta2` `APIService` objects and needs RBAC to
`get/list/watch` nodes, namespaces, and PVCs plus `get` on `nodes/stats` and
`nodes/proxy`. `--kubelet-*` flags control how it authenticates to and
addresses each kubelet. See `manifests/storage-metrics-server/` (kustomize)
and the Helm chart in `kubeops.dev/installer` at
`charts/storage-metrics-server`; the two must stay in lockstep with the
binary's flag surface.
