# TEST.md

How to test the **Kubernetes Storage Metrics Server** — unit tests for the
storage-metrics code and manual/end-to-end verification against a live
cluster. For architecture see [DESIGN.md](DESIGN.md); for development
conventions see [DEV.md](DEV.md).

## Unit tests

The storage-metrics logic is covered by table-driven Go tests that need only a
local Go toolchain (no cluster, no Docker):

```bash
# all storage-metrics packages
go test ./pkg/storagemetrics/...

# a single package, verbose
go test ./pkg/storagemetrics/scraper/... -v

# a single test
go test ./pkg/storagemetrics/scraper/... -run TestScrape_PreferFreshestForRWXPVC -v

# with the race detector (recommended — the scraper and cache are concurrent)
go test -race ./pkg/storagemetrics/...
```

`make unit-tests` (alias `make test`) runs the full module's tests inside the
`ghcr.io/appscode/golang-dev` container and therefore needs Docker running;
the plain `go test` commands above are the fast inner-loop equivalent for the
storage-metrics packages.

### What the unit tests cover

| Package | Tests | What they pin down |
| --- | --- | --- |
| `scraper/client` | `TestSummaryToBatch_*`, `TestSummaryClient_GetMetrics_HTTP` | non-PVC volumes skipped; zero `FsStats.Time` falls back to request time; freshest sample per PVC kept; end-to-end HTTP decode against a stub `/stats/summary` server |
| `scraper` | `TestScrape_MergesNodes`, `TestScrape_PreferFreshestForRWXPVC`, `TestScrape_NodeErrorIsTolerated` | per-node batches merge; RWX dedup keeps the newest; one failing node doesn't sink the tick |
| `storage` | `TestStorage_StoreAndGet`, `TestStorage_ListNamespace` | atomic swap, `Get`, per-namespace listing |
| `provider` | `TestProvider_GetMetricByName*`, `TestProvider_GetMetricBySelector_SkipsUnscrapedPVCs`, `TestProvider_ListAllMetrics` | value lookup, unknown PVC → NotFound, wrong resource rejected, percentage returned in milli units, selector skips un-scraped PVCs, discovery lists all metrics |

When you add or change a metric, extend the `provider` and `scraper/client`
tests — the metric names are a user contract (see [DEV.md](DEV.md)).

## Build & lint checks

```bash
go build ./...          # compiles the binary and library
go vet ./pkg/storagemetrics/... ./cmd/storage-metrics-server/...

make ci                 # full pipeline: check-license lint build unit-tests (Docker)
make lint               # golangci-lint (Docker)
make verify             # verify-gen verify-modules; go mod tidy && vendor must be clean
```

## Manual / end-to-end testing against a cluster

You need a cluster whose kubelets expose `/stats/summary` (any real cluster;
`kind`/`minikube` work) and at least one **bound, mounted** PVC — a PVC that
is not attached to a running pod is never reported by kubelet and will not
appear.

### 1. Deploy a workload with a PVC

```bash
kubectl apply -f consumer/sample_pvc.yaml   # nginx Deployment + 5Gi PVC "app-storage"
```

### 2. Install the server

Kustomize bundle:

```bash
kubectl apply -k manifests/storage-metrics-server
```

or Helm (chart is published to the `ghcr.io/appscode-charts` OCI registry):

```bash
kubectl create namespace storage-metrics
helm install storage-metrics-server \
  oci://ghcr.io/appscode-charts/storage-metrics-server \
  --version v0.1.0 \
  --namespace storage-metrics
```

Wait for the APIService to go `Available`:

```bash
kubectl get apiservice v1beta2.custom.metrics.k8s.io
kubectl -n storage-metrics rollout status deploy/storage-metrics-server
```

### 3. Verify discovery

The eight `volume_*` metrics should be listed:

```bash
kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta2" | jq '.resources[].name'
```

### 4. Query a PVC metric

```bash
kubectl get --raw \
  "/apis/custom.metrics.k8s.io/v1beta2/namespaces/default/persistentvolumeclaims/app-storage/volume_used_percentage" \
  | jq .
```

`volume_used_percentage` is in milli units — `250` means 25 %. Give it up to
one `--metric-resolution` interval (default 60s) after install/mount before
values appear.

### 5. Cross-check against the raw kubelet data

Confirm the server's numbers match what kubelet reports directly:

```bash
kubectl get --raw /api/v1/nodes/<node>/proxy/stats/summary | \
  jq '[.pods[] | select(.volume != null)
        | {pod: .podRef.name,
           vols: [.volume[] | select(.pvcRef != null) | {name, pvcRef, capacityBytes, usedBytes}]}]
      | map(select(.vols | length > 0))'
```

(See `consumer/README.md` for this snippet and a from-source image build.)

### 6. Exercise it from an HPA (optional)

`consumer/` contains an example client (`main.go`) and PVC workload. Point an
`autoscaling/v2` HPA at an `Object` metric named e.g. `volume_used_percentage`
with `describedObject` a `PersistentVolumeClaim`, then fill the volume and
watch the HPA pick up the value.

## Troubleshooting

- **No metrics for a PVC** — it must be bound *and* mounted by a running pod;
  unmounted/unbound PVCs are never in `/stats/summary`.
- **`Metric not found` on a single-PVC query but the selector query is empty** —
  the cache hasn't been populated yet; wait one scrape interval.
- **kubelet scrape errors in the logs** (`-v=2` shows the scrape summary) —
  usually TLS: use `--kubelet-insecure-tls` for test clusters, or point
  `--kubelet-certificate-authority` at the right CA. Check the
  `--kubelet-preferred-address-types` ordering if the server can't reach node
  addresses.
- **A percentage metric is missing** — kubelet omitted the backing field
  (or capacity/inode total is 0) for that driver; the server returns
  NotFound rather than a misleading 0. Check the raw summary from step 5.
