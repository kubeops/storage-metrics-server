# AGENTS.md

This file provides guidance to coding agents (e.g. Claude Code, claude.ai/code) when working with code in this repository.

## Repository purpose

A **fork of [kubernetes-sigs/custom-metrics-apiserver](https://github.com/kubernetes-sigs/custom-metrics-apiserver)** that adds the AppsCode **Kubernetes Storage Metrics Server** — a Custom Metrics API server that exposes per-PVC capacity and usage metrics so HPA / VPA can autoscale based on volume utilization. The base library + the storage-metrics binary live in the same repo so the upstream rebase stays straightforward.

What the storage-metrics server exposes under `custom.metrics.k8s.io/v1beta2` on the `persistentvolumeclaims` resource:

- `volume_capacity_bytes`, `volume_available_bytes`, `volume_used_bytes`
- `volume_used_percentage` (milli units; 1000m == 100%)
- `volume_inodes`, `volume_inodes_free`, `volume_inodes_used`, `volume_inodes_used_percentage`

It scrapes each node's kubelet `/stats/summary` endpoint on a configurable interval (default 60s). Reading from `/stats/summary` (instead of the CSI `NodeGetVolumeStats`) means it works for **any kubelet-mounted filesystem PVC** — in-tree, external CSI, migrated, and pre-existing PVCs.

The Go module path is `kubeops.dev/storage-metrics-apiserver` (renamed from the upstream `sigs.k8s.io/custom-metrics-apiserver`). Git remote is `kubeops/storage-metrics-apiserver`. The produced binary is `storage-metrics-apiserver`.

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
- `manifests/` — kustomize-style install manifests.

The Helm chart lives in the sibling `kubeops.dev/installer` repo at
`charts/storage-metrics-apiserver` (not in this repo); `make install` deploys it from `../installer`.

### Build harness

- `Makefile`, `hack/` — the AppsCode/kubeops build harness (shared with e.g. `kubeops.dev/petset`);
  most targets run inside the `ghcr.io/appscode/golang-dev` image, so Docker must be running.
- `Dockerfile.in` (PROD, alpine), `Dockerfile.dbg` (debian + dlv), `Dockerfile.ubi` (Red Hat) —
  three image variants; keep them in sync.
- `.github/workflows/` — `ci.yml`, `release.yml`, `release-tracker.yml`.
- `OWNERS`, `code-of-conduct.md`, `SECURITY_CONTACTS`, `CONTRIBUTING.md`, `RELEASE.md` — upstream community files.
- `vendor/` — checked-in deps.

## Common commands

Consult `Makefile` for the full target set. Docker must be running.

- `make ci` — CI pipeline (`check-license lint build unit-tests`).
- `make build` / `make all-build` — build the host binary / all-platform binaries.
- `make fmt`, `make lint`, `make unit-tests` / `make test` — standard.
- `make gen` — regenerate the committed `pkg/generated/openapi/*/zz_generated.openapi.go`
  (this fork has no CRD/clientset codegen).
- `make verify` — `verify-gen verify-modules`; `go mod tidy && go mod vendor` must leave the tree clean.
- `make container` / `make push` / `make docker-manifest` / `make release` — image build & publish flow.
- `make install` / `make uninstall` — Helm install lifecycle from `../installer`.
- `make add-license` / `make check-license` — manage AppsCode license headers.

Run a single Go test (requires a local Go toolchain):

```
go test ./pkg/storagemetrics/scraper/... -run TestName -v
```

## Conventions

- Module path is `kubeops.dev/storage-metrics-apiserver`. Imports must use that. (It was renamed from the upstream `sigs.k8s.io/custom-metrics-apiserver`; when rebasing against upstream, re-apply this rename to any newly pulled imports.)
- **Upstream-tracking** fork. Prefer rebasing onto upstream over diverging. All AppsCode-specific code lives under `pkg/storagemetrics/` and `cmd/storage-metrics-apiserver/`; do not modify upstream packages (`pkg/apiserver/`, `pkg/registry/`, `pkg/provider/`, `pkg/cmd/`, `pkg/dynamicmapper/`, `pkg/generated/`) unless rebasing.
- License: Apache-2.0 (`LICENSE`).
- Sign off commits (`git commit -s`); contributions follow `CONTRIBUTING.md`.
- The custom-metrics API surface (`custom.metrics.k8s.io/v1beta2`) is a **stable Kubernetes API** — do not break the wire format.
- Metric names in `pkg/storagemetrics/provider/` are the **user contract** for HPAs that reference them. Don't rename without a coordinated migration.
- The scraper reads `/stats/summary` from each node's kubelet. That choice is load-bearing for the "works on legacy PVCs" property; switching to `NodeGetVolumeStats` would break old in-tree volumes.
- The Helm chart (in `kubeops.dev/installer` at `charts/storage-metrics-apiserver`) and the in-repo
  `manifests/` must stay in lockstep with the binary's flag surface.
