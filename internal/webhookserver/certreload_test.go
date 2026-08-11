/*
Copyright 2026 The declarative-conversion-operator Authors.

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
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSelfSignedCert generates a throwaway self-signed ECDSA certificate
// and writes tls.crt/tls.key into dir, mimicking what cert-manager mounts.
func writeSelfSignedCert(t *testing.T, dir string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}

	var certBuf, keyBuf bytes.Buffer
	if err := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encoding cert: %v", err)
	}
	if err := pem.Encode(&keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatalf("encoding key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), certBuf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing tls.crt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), keyBuf.Bytes(), 0o600); err != nil {
		t.Fatalf("writing tls.key: %v", err)
	}
}

func TestNewCertReloader_LoadsInitialCertificate(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedCert(t, dir, time.Now().Add(24*time.Hour))

	r, err := NewCertReloader(dir, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cert, err := r.GetCertificate(nil)
	if err != nil {
		t.Fatalf("unexpected error from GetCertificate: %v", err)
	}
	if cert == nil {
		t.Fatalf("expected a non-nil certificate")
	}
	if r.LastError() != nil {
		t.Fatalf("expected no reload error, got %v", r.LastError())
	}
}

func TestNewCertReloader_MissingFiles_Errors(t *testing.T) {
	dir := t.TempDir() // empty: no tls.crt/tls.key
	if _, err := NewCertReloader(dir, time.Hour); err == nil {
		t.Fatalf("expected an error when the certificate files don't exist")
	}
}

func TestCertReloader_GetCertificate_BeforeAnyLoad(t *testing.T) {
	r := &CertReloader{}
	if _, err := r.GetCertificate(nil); err == nil {
		t.Fatalf("expected an error before any certificate has been loaded")
	}
}

func TestCertReloader_Reload_PicksUpRotatedCertificate(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedCert(t, dir, time.Now().Add(24*time.Hour))

	r, err := NewCertReloader(dir, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	first, _ := r.GetCertificate(nil)

	// Rotate: write a brand-new cert/key pair over the same paths.
	writeSelfSignedCert(t, dir, time.Now().Add(48*time.Hour))
	if err := r.reload(); err != nil {
		t.Fatalf("unexpected reload error: %v", err)
	}
	second, _ := r.GetCertificate(nil)
	if bytes.Equal(first.Certificate[0], second.Certificate[0]) {
		t.Fatalf("expected the reloaded certificate to differ from the original")
	}
}

func TestCertReloader_Reload_FailurePreservesLastGood(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedCert(t, dir, time.Now().Add(24*time.Hour))
	r, err := NewCertReloader(dir, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	good, _ := r.GetCertificate(nil)

	// Corrupt the key file so the next reload fails.
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatalf("corrupting key: %v", err)
	}
	if err := r.reload(); err == nil {
		t.Fatalf("expected reload to fail against a corrupted key")
	}
	if r.LastError() == nil {
		t.Fatalf("expected LastError to be set after a failed reload")
	}

	stillGood, _ := r.GetCertificate(nil)
	if !bytes.Equal(good.Certificate[0], stillGood.Certificate[0]) {
		t.Fatalf("expected the last known-good certificate to keep being served after a failed reload")
	}
}

func TestCertReloader_Run_StopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedCert(t, dir, time.Now().Add(24*time.Hour))
	r, err := NewCertReloader(dir, time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	// Let at least one tick fire against a real cert-reload cycle before
	// stopping, so this also incidentally exercises Run's ticker branch.
	time.Sleep(5 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expected Run to return promptly after context cancellation")
	}
}
