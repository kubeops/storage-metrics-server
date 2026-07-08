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

// Package options exposes the command-line flags for the kubelet
// /stats/summary scraper used by the storage metrics apiserver.
package options

import (
	"fmt"
	"time"

	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/scraper/client"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
)

// KubeletClientOptions configures how the apiserver talks to each kubelet.
// Defaults match what metrics-server ships with so operators can reuse the
// same flags they already know.
type KubeletClientOptions struct {
	KubeletUseNodeStatusPort     bool
	KubeletPort                  int
	InsecureKubeletTLS           bool
	KubeletPreferredAddressTypes []string
	KubeletCAFile                string
	KubeletClientKeyFile         string
	KubeletClientCertFile        string
	KubeletRequestTimeout        time.Duration
	NodeSelector                 string
}

// NewKubeletClientOptions returns the default kubelet client options.
func NewKubeletClientOptions() *KubeletClientOptions {
	o := &KubeletClientOptions{
		KubeletPort:           10250,
		KubeletRequestTimeout: 10 * time.Second,
	}
	o.KubeletPreferredAddressTypes = make([]string, len(client.DefaultAddressTypePriority))
	for i, t := range client.DefaultAddressTypePriority {
		o.KubeletPreferredAddressTypes[i] = string(t)
	}
	return o
}

// AddFlags binds the flags onto the given FlagSet.
func (o *KubeletClientOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.InsecureKubeletTLS, "kubelet-insecure-tls", o.InsecureKubeletTLS,
		"Do not verify CA of serving certificates presented by Kubelets. For testing only.")
	fs.BoolVar(&o.KubeletUseNodeStatusPort, "kubelet-use-node-status-port", o.KubeletUseNodeStatusPort,
		"Use the port reported in node.status. Takes precedence over --kubelet-port.")
	fs.IntVar(&o.KubeletPort, "kubelet-port", o.KubeletPort, "Port to use to connect to Kubelets.")
	fs.StringSliceVar(&o.KubeletPreferredAddressTypes, "kubelet-preferred-address-types",
		o.KubeletPreferredAddressTypes, "Priority list of node address types used to dial Kubelets.")
	fs.StringVar(&o.KubeletCAFile, "kubelet-certificate-authority", "",
		"Path to the CA used to validate the Kubelet's serving certificates.")
	fs.StringVar(&o.KubeletClientKeyFile, "kubelet-client-key", "", "Path to a client key file for TLS.")
	fs.StringVar(&o.KubeletClientCertFile, "kubelet-client-certificate", "", "Path to a client cert file for TLS.")
	fs.DurationVar(&o.KubeletRequestTimeout, "kubelet-request-timeout", o.KubeletRequestTimeout,
		"Timeout for a single kubelet stats/summary request.")
	fs.StringVarP(&o.NodeSelector, "node-selector", "l", o.NodeSelector,
		"Label selector used to filter which nodes are scraped (e.g. 'role=worker').")
}

// Validate returns any errors discovered in the option values.
func (o *KubeletClientOptions) Validate() []error {
	var errs []error
	if o.KubeletCAFile != "" && o.InsecureKubeletTLS {
		errs = append(errs, fmt.Errorf("cannot use both --kubelet-certificate-authority and --kubelet-insecure-tls"))
	}
	if (o.KubeletClientKeyFile != "") != (o.KubeletClientCertFile != "") {
		errs = append(errs, fmt.Errorf("need both --kubelet-client-key and --kubelet-client-certificate"))
	}
	if o.KubeletRequestTimeout <= 0 {
		errs = append(errs, fmt.Errorf("--kubelet-request-timeout must be positive"))
	}
	if o.KubeletPort <= 0 || o.KubeletPort > 65535 {
		errs = append(errs, fmt.Errorf("--kubelet-port must be in [1, 65535]"))
	}
	return errs
}

// Config converts the option values into a kubelet client config, given
// the REST config used to discover credentials.
func (o KubeletClientOptions) Config(restConfig *rest.Config) *client.KubeletClientConfig {
	cfg := &client.KubeletClientConfig{
		Scheme:              "https",
		DefaultPort:         o.KubeletPort,
		AddressTypePriority: o.addressTypes(),
		UseNodeStatusPort:   o.KubeletUseNodeStatusPort,
		Client:              *rest.CopyConfig(restConfig),
	}
	if o.InsecureKubeletTLS {
		cfg.Client.TLSClientConfig.Insecure = true
		cfg.Client.TLSClientConfig.CAData = nil
		cfg.Client.TLSClientConfig.CAFile = ""
	}
	if o.KubeletCAFile != "" {
		cfg.Client.TLSClientConfig.CAFile = o.KubeletCAFile
		cfg.Client.TLSClientConfig.CAData = nil
	}
	if o.KubeletClientCertFile != "" {
		cfg.Client.TLSClientConfig.CertFile = o.KubeletClientCertFile
		cfg.Client.TLSClientConfig.CertData = nil
	}
	if o.KubeletClientKeyFile != "" {
		cfg.Client.TLSClientConfig.KeyFile = o.KubeletClientKeyFile
		cfg.Client.TLSClientConfig.KeyData = nil
	}
	cfg.Client.Timeout = o.KubeletRequestTimeout
	return cfg
}

func (o KubeletClientOptions) addressTypes() []corev1.NodeAddressType {
	out := make([]corev1.NodeAddressType, len(o.KubeletPreferredAddressTypes))
	for i, t := range o.KubeletPreferredAddressTypes {
		out[i] = corev1.NodeAddressType(t)
	}
	return out
}

// StorageMetricsOptions covers flags specific to the scrape loop itself.
type StorageMetricsOptions struct {
	MetricResolution time.Duration
}

// NewStorageMetricsOptions returns the default storage-metrics options.
func NewStorageMetricsOptions() *StorageMetricsOptions {
	return &StorageMetricsOptions{MetricResolution: 60 * time.Second}
}

// AddFlags binds storage-metrics flags to a FlagSet.
func (o *StorageMetricsOptions) AddFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&o.MetricResolution, "metric-resolution", o.MetricResolution,
		"How often to scrape kubelets for PVC volume metrics. Must be at least 10s.")
}

// Validate returns any errors with the storage-metrics options.
func (o *StorageMetricsOptions) Validate() []error {
	var errs []error
	if o.MetricResolution < 10*time.Second {
		errs = append(errs, fmt.Errorf("--metric-resolution must be at least 10s, got %v", o.MetricResolution))
	}
	return errs
}
