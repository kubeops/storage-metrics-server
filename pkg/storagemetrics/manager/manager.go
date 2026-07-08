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

// Package manager runs the periodic scrape loop that drives the in-memory
// PVC storage metrics cache.
package manager

import (
	"context"
	"sync"
	"time"

	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/scraper"
	"kubeops.dev/storage-metrics-server/pkg/storagemetrics/storage"

	"k8s.io/klog/v2"
)

// Manager owns the scrape loop. It writes each tick's batch into the
// shared Storage so the API server can serve it without blocking.
type Manager struct {
	scraper    scraper.Scraper
	store      *storage.Storage
	resolution time.Duration

	mu            sync.RWMutex
	tickLastStart time.Time
}

// NewManager wires a scraper to a storage cache and a tick interval. The
// scrape loop is started by RunUntil.
func NewManager(s scraper.Scraper, store *storage.Storage, resolution time.Duration) *Manager {
	return &Manager{
		scraper:    s,
		store:      store,
		resolution: resolution,
	}
}

// RunUntil ticks at Manager.resolution. The first scrape happens immediately
// so the cache is populated as soon as possible after startup.
func (m *Manager) RunUntil(ctx context.Context) {
	ticker := time.NewTicker(m.resolution)
	defer ticker.Stop()

	m.tick(ctx, time.Now())
	for {
		select {
		case t := <-ticker.C:
			m.tick(ctx, t)
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) tick(ctx context.Context, start time.Time) {
	m.mu.Lock()
	m.tickLastStart = start
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, m.resolution)
	defer cancel()

	batch := m.scraper.Scrape(ctx)
	m.store.Store(batch)
	klog.V(4).InfoS("Storage metrics tick complete", "duration", time.Since(start), "pvcs", m.store.Count())
}

// LastTickStart reports when the last scrape tick began. Healthchecks use
// this to detect a stalled loop.
func (m *Manager) LastTickStart() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tickLastStart
}
