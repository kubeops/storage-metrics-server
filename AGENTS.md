# AGENTS.md

This file provides guidance to coding agents (e.g. Claude Code, claude.ai/code) when working with code in this repository.

## Repository purpose

A **fork of [kubernetes-sigs/custom-metrics-apiserver](https://github.com/kubernetes-sigs/custom-metrics-apiserver)** that adds the AppsCode **Kubernetes Storage Metrics Server** — a Custom Metrics API server that exposes per-PVC capacity and usage metrics so HPA / VPA can autoscale based on volume utilization. The base library + the storage-metrics binary live in the same repo so the upstream rebase stays straightforward.

What the storage-metrics server exposes under `custom.metrics.k8s.io/v1beta2` on the `persistentvolumeclaims` resource:

- `volume_capacity_bytes`, `volume_available_bytes`, `volume_used_bytes`
- `volume_used_percentage` (milli units; 1000m == 100%)
- `volume_inodes`, `volume_inodes_free`, `volume_inodes_used`, `volume_inodes_used_percentage`

It scrapes each node's kubelet `/stats/summary` endpoint on a configurable interval (default 60s). Reading from `/stats/summary` (instead of the CSI `NodeGetVolumeStats`) means it works for **any kubelet-mounted filesystem PVC** — in-tree, external CSI, migrated, and pre-existing PVCs.

The Go module path stays at upstream: `sigs.k8s.io/custom-metrics-apiserver`. Git remote is `kubeops/storage-metrics-apiserver`. The produced binary is `storage-metrics-apiserver`.

## Architecture

### Upstream base (custom-metrics-apiserver)

- `pkg/apiserver/` — the upstream aggregated apiserver foundation.
- `pkg/registry/` — REST storage for custom-metrics resources.
- `pkg/provider/` — interfaces the storage-metrics implementation plugs into.
- `pkg/cmd/` — common cmd plumbing reused by downstream binaries.
- `pkg/dynamicmapper/` — REST mapper that watches discovery for new GVKs.
- `pkg/generated/` — generated clientset.
- `cmd/`, `test-adapter/`, `test-adapter-deploy/` — upstream's reference test-adapter implementation; kept so upstream PRs apply cleanly.

### Storage-metrics addition

- `cmd/storage-metrics-apiserver/` — the new binary (`main.go`, `Dockerfile`).
- `pkg/storagemetrics/`:
  - `manager/` — server bootstrap.
  - `options/` — CLI flags (scrape interval, kubelet auth, etc.).
  - `provider/` — implements `pkg/provider.MetricsProvider` for PVC metrics.
  - `scraper/` — kubelet `/stats/summary` client (the scraper that runs every interval).
  - `storage/` — in-memory storage for last-seen metrics.
- `consumer/` — example HPA / VPA configurations consuming the metrics.
- `charts/` — Helm chart for installing the server.
- `manifests/` — kustomize-style install manifests.

### Upstream housekeeping

- `Makefile` — upstream Makefile (local Go toolchain).
- `OWNERS`, `code-of-conduct.md`, `SECURITY_CONTACTS`, `CONTRIBUTING.md`, `RELEASE.md` — upstream community files.
- `vendor/` — checked-in deps.

## Common commands

This repo uses the **upstream Makefile** (local Go toolchain). Consult `Makefile` for the exact target set.

- `make build` — Go build.
- `make test` — Go tests.
- `make verify` — lint / verify.
- `make help` — list targets.

Run a single Go test:

```
go test ./pkg/storagemetrics/scraper/... -run TestName -v
```

## Conventions

- Module path is `sigs.k8s.io/custom-metrics-apiserver` (**upstream**). Imports must use that, not the GitHub URL.
- **Upstream-tracking** fork. Prefer rebasing onto upstream over diverging. All AppsCode-specific code lives under `pkg/storagemetrics/` and `cmd/storage-metrics-apiserver/`; do not modify upstream packages (`pkg/apiserver/`, `pkg/registry/`, `pkg/provider/`, `pkg/cmd/`, `pkg/dynamicmapper/`, `pkg/generated/`) unless rebasing.
- License: Apache-2.0 (`LICENSE`).
- Sign off commits (`git commit -s`); contributions follow `CONTRIBUTING.md`.
- The custom-metrics API surface (`custom.metrics.k8s.io/v1beta2`) is a **stable Kubernetes API** — do not break the wire format.
- Metric names in `pkg/storagemetrics/provider/` are the **user contract** for HPAs that reference them. Don't rename without a coordinated migration.
- The scraper reads `/stats/summary` from each node's kubelet. That choice is load-bearing for the "works on legacy PVCs" property; switching to `NodeGetVolumeStats` would break old in-tree volumes.
- The Helm chart in `charts/` and manifests in `manifests/` must stay in lockstep with the binary's flag surface.
