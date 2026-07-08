
## Build & Push

```bash
docker build -t ghcr.io/arnobkumarsaha/storage-metrics-server:dev \
  --build-arg TARGETARCH=amd64 \
  -f cmd/storage-metrics-server/Dockerfile .

docker push ghcr.io/arnobkumarsaha/storage-metrics-server:dev
```

## Usage

List available custom metrics:

```bash
kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta2" | jq '.resources[].name'
```

Query a specific PVC metric:

```bash
kubectl get --raw "/apis/custom.metrics.k8s.io/v1beta2/namespaces/default/persistentvolumeclaims/app-storage/volume_used_bytes" | jq .
```

Inspect kubelet stats directly for PVC volumes on a node:

```bash
kubectl get --raw /api/v1/nodes/real-spoke1/proxy/stats/summary | \
  jq '[.pods[] | select(.volume != null) | {pod: .podRef.name, vols: [.volume[] | select(.pvcRef != null) | {name, pvcRef}]}] | map(select(.vols | length > 0))'
```
