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

// Command storage-metrics-server runs a Kubernetes Custom Metrics API
// server that exposes PVC storage capacity, usage and inode metrics
// scraped from each node's kubelet /stats/summary endpoint.
//
// It is intended for storage autoscaling: an HPA or operator can read
// volume_used_percentage / volume_used_bytes for a PVC the same way it
// reads CPU or memory from metrics-server.
package main

import (
	"context"
	"os"
	"time"

	apiservermetrics "kubeops.dev/storage-metrics-server/pkg/apiserver/metrics"
	basecmd "kubeops.dev/storage-metrics-server/pkg/cmd"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/manager"
	smoptions "kubeops.dev/storage-metrics-server/pkg/storagemetrics/options"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/provider"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/scraper"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/scraper/client"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/storage"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/component-base/logs"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
)

// Build metadata stamped in via -ldflags -X by hack/build.sh.
var (
	Version         string
	VersionStrategy string
	GitTag          string
	GitBranch       string
	CommitHash      string
	CommitTimestamp string
	GoVersion       string
	Compiler        string
	Platform        string
)

// StorageAdapter wires the kubelet scraper, in-memory cache and custom
// metrics provider into a single binary built on top of AdapterBase.
type StorageAdapter struct {
	basecmd.AdapterBase

	KubeletOptions *smoptions.KubeletClientOptions
	ScrapeOptions  *smoptions.StorageMetricsOptions
}

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()

	klog.Infof("storage-metrics-server version=%q strategy=%q commit=%q built=%q go=%q platform=%q",
		Version, VersionStrategy, CommitHash, CommitTimestamp, GoVersion, Platform)

	a := &StorageAdapter{
		KubeletOptions: smoptions.NewKubeletClientOptions(),
		ScrapeOptions:  smoptions.NewStorageMetricsOptions(),
	}
	a.Name = "storage-metrics-server"

	// AdapterBase.Flags() seeds the recommended-options flags. We append our
	// own kubelet/scraper flags onto the same FlagSet.
	a.KubeletOptions.AddFlags(a.Flags())
	a.ScrapeOptions.AddFlags(a.Flags())
	logs.AddFlags(a.Flags())

	if err := a.Flags().Parse(os.Args); err != nil {
		klog.Fatalf("unable to parse flags: %v", err)
	}
	if errs := a.KubeletOptions.Validate(); len(errs) > 0 {
		klog.Fatalf("invalid kubelet options: %v", errs)
	}
	if errs := a.ScrapeOptions.Validate(); len(errs) > 0 {
		klog.Fatalf("invalid scrape options: %v", errs)
	}

	if err := a.run(); err != nil {
		klog.Fatalf("storage-metrics-server: %v", err)
	}
}

func (a *StorageAdapter) run() error {
	restConfig, err := a.ClientConfig()
	if err != nil {
		return err
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return err
	}

	// Use a separate informer factory for Nodes/PVCs because AdapterBase's
	// SharedInformerFactory is wired to the apiserver lifecycle and we want
	// the scraper to start ticking even while the API server is still
	// preparing handlers.
	informerFactory := informers.NewSharedInformerFactory(kubeClient, 10*time.Minute)
	nodeInformer := informerFactory.Core().V1().Nodes()
	pvcInformer := informerFactory.Core().V1().PersistentVolumeClaims()

	kubeletCfg := a.KubeletOptions.Config(restConfig)
	kubeletClient, err := client.NewForConfig(kubeletCfg)
	if err != nil {
		return err
	}

	var nodeReqs []labels.Requirement
	if a.KubeletOptions.NodeSelector != "" {
		sel, err := labels.Parse(a.KubeletOptions.NodeSelector)
		if err != nil {
			return err
		}
		reqs, _ := sel.Requirements()
		nodeReqs = reqs
	}

	store := storage.NewStorage()
	scr := scraper.NewScraper(nodeInformer.Lister(), kubeletClient,
		a.KubeletOptions.KubeletRequestTimeout, nodeReqs)
	mgr := manager.NewManager(scr, store, a.ScrapeOptions.MetricResolution)

	prov := provider.NewProvider(store, provider.FromInformerLister(pvcInformer.Lister()))
	a.WithCustomMetrics(prov)

	if err := apiservermetrics.RegisterMetrics(legacyregistry.Register); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	informerFactory.Start(ctx.Done())
	klog.Info("waiting for Node and PVC informer caches to sync")
	if !cache.WaitForCacheSync(ctx.Done(),
		nodeInformer.Informer().HasSynced,
		pvcInformer.Informer().HasSynced,
	) {
		klog.Fatal("informer caches did not sync")
	}
	klog.Info("informer caches synced; starting scrape loop")

	go mgr.RunUntil(ctx)

	return a.Run(ctx)
}
