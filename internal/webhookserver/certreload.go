/*
Copyright 2026 The xrd-conversion-operator Authors.

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

package webhookserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// CertReloader serves the TLS certificate/key mounted from a cert-manager
// Secret, reloading it periodically from disk so a certificate rotation is
// picked up without a pod restart. Polling (rather than an fsnotify watch)
// is deliberate: it needs no extra dependency, and cert-manager renews well
// ahead of expiry, so a bounded poll interval is more than fast enough.
type CertReloader struct {
	certPath, keyPath string
	interval          time.Duration

	cert atomic.Pointer[tls.Certificate]

	mu      sync.Mutex
	lastErr error
}

// NewCertReloader loads tls.crt/tls.key from dir once synchronously (so
// startup fails fast if the mount is missing or invalid) and returns a
// reloader ready to serve GetCertificate.
func NewCertReloader(dir string, interval time.Duration) (*CertReloader, error) {
	r := &CertReloader{
		certPath: filepath.Join(dir, "tls.crt"),
		keyPath:  filepath.Join(dir, "tls.key"),
		interval: interval,
	}
	if err := r.reload(); err != nil {
		return nil, fmt.Errorf("loading initial TLS certificate: %w", err)
	}
	return r, nil
}

func (r *CertReloader) reload() error {
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		r.mu.Lock()
		r.lastErr = err
		r.mu.Unlock()
		return err
	}
	r.cert.Store(&cert)
	r.mu.Lock()
	r.lastErr = nil
	r.mu.Unlock()
	return nil
}

// GetCertificate implements tls.Config.GetCertificate.
func (r *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := r.cert.Load()
	if c == nil {
		return nil, fmt.Errorf("no TLS certificate loaded yet")
	}
	return c, nil
}

// LastError returns the most recent reload error, if any, for surfacing on
// a debug/health endpoint.
func (r *CertReloader) LastError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Run polls for certificate changes until ctx is done. A failed reload is
// not fatal — it just means the previously loaded (still valid)
// certificate keeps serving until the next successful reload.
func (r *CertReloader) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.reload()
		}
	}
}
