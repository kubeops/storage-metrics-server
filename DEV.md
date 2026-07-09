# DEV.md

What a Go developer needs to know to work on this repository. Pairs with
[AGENTS.md](AGENTS.md) (the same guidance, condensed for coding agents),
[DESIGN.md](DESIGN.md) (how/why the server works), and [TEST.md](TEST.md)
(how to test).

## What this repo is

A **fork of
[`kubernetes-sigs/custom-metrics-apiserver`](https://github.com/kubernetes-sigs/custom-metrics-apiserver)**
that adds the AppsCode **Storage Metrics Server** binary. The upstream library
and the new binary share one repo so upstream rebases stay simple.

- Go module path: **`kubeops.dev/storage-metrics-server`** (renamed from the
  upstream `sigs.k8s.io/custom-metrics-apiserver`).
- Produced binary: **`storage-metrics-server`** (`cmd/storage-metrics-server/`).
- Go: see `go.mod` (currently `go 1.25`). A local Go toolchain is enough for
  building and unit-testing the storage-metrics packages; the `make` harness
  needs Docker.

## Repository layout

### Upstream base — treat as vendored; don't touch except when rebasing

```
pkg/apiserver/       aggregated apiserver foundation
pkg/registry/        REST storage for custom-metrics resources
pkg/provider/        provider interfaces (CustomMetricsProvider, …) we implement
pkg/cmd/             AdapterBase cmd plumbing our binary builds on
pkg/dynamicmapper/   REST mapper watching discovery for new GVKs
pkg/generated/       generated clientset + committed openapi
cmd/ test-adapter/ test-adapter-deploy/   upstream reference test-adapter
```

### The AppsCode addition — where your changes go

```
cmd/storage-metrics-server/     the binary (main.go, Dockerfile)
pkg/storagemetrics/
  options/    CLI flags (kubelet client + scrape interval) and validation
  scraper/
    client/   kubelet /stats/summary HTTP client, summary→PVC batch, addr resolver
    scraper.go  per-node goroutine fan-out + cross-node merge
  storage/    in-memory last-seen cache (Storage, MetricsBatch, PVCMetricsPoint)
  manager/    tick loop: scrape → store, with staleness accessor
  provider/   provider.CustomMetricsProvider implementation (metrics.go = names/units)
consumer/     example HPA/VPA client + sample PVC workload
manifests/storage-metrics-server/   kustomize install bundle
```

The Helm chart is **not** in this repo — it lives in the sibling
`kubeops.dev/installer` repo at `charts/storage-metrics-server`. `make install`
deploys it from `../installer`.

## Conventions (the ones that bite)

- **Module path.** All imports use `kubeops.dev/storage-metrics-server/...`.
  When rebasing against upstream, re-apply the module rename to any newly
  pulled imports of `sigs.k8s.io/custom-metrics-apiserver`.
- **Upstream-tracking fork.** Prefer rebasing onto upstream over diverging. Put
  **all** AppsCode-specific code under `pkg/storagemetrics/` and
  `cmd/storage-metrics-server/`. Do **not** modify the upstream packages listed
  above unless you are doing a rebase — divergence there makes future rebases
  painful.
- **Stable wire format.** `custom.metrics.k8s.io/v1beta2` is a stable
  Kubernetes API. Don't change the response shape.
- **Metric names are a user contract.** The `Metric*` constants in
  `pkg/storagemetrics/provider/metrics.go` are referenced by users' HPAs. Don't
  rename them without a coordinated migration.
- **`/stats/summary` is load-bearing.** Don't swap it for CSI
  `NodeGetVolumeStats` — that would break metrics for legacy in-tree volumes
  and drivers without that RPC. See [DESIGN.md](DESIGN.md) §1.
- **Manifests ↔ flags lockstep.** The in-repo `manifests/` and the installer
  Helm chart must track the binary's flag surface. If you add/rename a flag,
  update both (and coordinate the chart change in `kubeops.dev/installer`).
- **License headers.** Every Go file carries the Apache-2.0 AppsCode header;
  `make add-license` adds them, `make check-license` verifies.
- **Sign off commits** — `git commit -s` (a `Signed-off-by` trailer is
  required; see `CONTRIBUTING.md`).

## Common commands

Full set is in the `Makefile`; most `make` targets run inside
`ghcr.io/appscode/golang-dev`, so **Docker must be running**.

| Command | Does |
| --- | --- |
| `go build ./...` / `go test ./pkg/storagemetrics/...` | fast local inner loop |
| `make build` / `make all-build` | host binary / all-platform binaries |
| `make ci` | `check-license lint build unit-tests` |
| `make fmt` / `make lint` | format / golangci-lint |
| `make unit-tests` (`make test`) | full module tests in-container |
| `make gen` | regenerate committed `pkg/generated/openapi/*/zz_generated.openapi.go` (no CRD/clientset codegen in this fork) |
| `make verify` | `verify-gen verify-modules`; `go mod tidy && go mod vendor` must leave the tree clean |
| `make container` / `make push` / `make docker-manifest` / `make release` | image build & publish |
| `make install` / `make uninstall` | Helm lifecycle from `../installer` |
| `make add-license` / `make check-license` | license headers |

Dependencies are vendored (`vendor/` is checked in); after changing `go.mod`,
run `go mod tidy && go mod vendor` and commit the result (`make verify`
enforces a clean tree).

## Flag surface

Defined in `pkg/storagemetrics/options/` and appended onto `AdapterBase`'s
recommended-options flag set in `main.go`.

- **Kubelet client** (defaults mirror `metrics-server`):
  `--kubelet-port` (10250), `--kubelet-use-node-status-port`,
  `--kubelet-insecure-tls`, `--kubelet-preferred-address-types`,
  `--kubelet-certificate-authority`, `--kubelet-client-key`,
  `--kubelet-client-certificate`, `--kubelet-request-timeout` (10s),
  `--node-selector`/`-l` (filter which nodes are scraped).
- **Scrape loop:** `--metric-resolution` (default 60s, validated ≥ 10s).
- Plus everything from `AdapterBase` (`--secure-port`, `--cert-dir`, `--v`, …).

`Validate()` rejects contradictory combinations (e.g. both
`--kubelet-certificate-authority` and `--kubelet-insecure-tls`, a lone client
key/cert, a sub-10s resolution).

## How a request is served (the mental model)

1. `manager.RunUntil` ticks every `--metric-resolution`, immediately on start.
2. Each tick, `scraper.Scrape` fans a goroutine out per Node, each calling
   `client.GetMetrics` → `GET /stats/summary`, then merges the per-node batches
   (freshest sample wins for RWX PVCs) into one `MetricsBatch`.
3. `storage.Store` atomically swaps that batch into the cache.
4. An incoming custom-metrics API call lands in `provider.GetMetricByName` /
   `GetMetricBySelector`, which reads the cache (and, for selectors, the live
   PVC informer) and builds `custom_metrics.MetricValue`s via `metricFromPoint`.

The scrape loop and the serve path share only `storage.Storage`. See
[DESIGN.md](DESIGN.md) for the reasoning behind each step.

## Worked example: adding a new PVC metric

Say kubelet's `FsStats` gained a field you want to expose:

1. **`storage/types.go`** — add the field to `PVCMetricsPoint` (and a
   `Has…` guard if kubelet may omit it).
2. **`scraper/client/summary_client.go`** — populate it in `summaryToBatch`
   from the summary field; set the guard.
3. **`provider/metrics.go`** — add a `MetricVolume…` constant, append it to
   `AllMetrics`, and add a `case` in `metricFromPoint` (pick `BinarySI` for
   bytes, `DecimalSI` for counts, `NewMilliQuantity` for percentages).
4. **Tests** — extend `summary_client_test.go` and `provider_test.go`.
5. **Docs** — update the metric tables in [README.md](README.md),
   [DESIGN.md](DESIGN.md), and [AGENTS.md](AGENTS.md).
6. Run `go test ./pkg/storagemetrics/... -race`.

Because the name becomes part of the user contract, treat step 3's constant as
permanent.

## Gotchas

- **Only bound + mounted PVCs appear** — kubelet reports a volume only while a
  pod mounts it. Unbound/unmounted PVCs are absent from the cache (and thus
  from selector results); a single-PVC `GetMetricByName` for one returns
  `MetricNotFound`.
- **Percentages are milli** — `1000m == 100%`. Don't "fix" this to whole
  percents; it preserves HPA precision.
- **Don't cache zeros** — an entry with no usable numbers is dropped at scrape
  time, and `metricFromPoint` returns NotFound when a backing field/denominator
  is missing. Preserve that behavior; serving `0` would mislead autoscalers.
- **Separate informer factory** — `main.go` uses its own Node/PVC
  `SharedInformerFactory`, not `AdapterBase`'s. Keep the scrape loop
  independent of the API server lifecycle.
- **Copyright header year** — new files use the AppsCode Apache-2.0 header;
  `make check-license` will flag a missing one in CI.
