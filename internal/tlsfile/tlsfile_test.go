// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package tlsfile_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

// Пара перечитывается после ротации; пара, записанная не до конца, не
// роняет держателя — действует прежняя. Серверный и клиентский доступ —
// одна и та же пара.
func TestKeyPair_ReloadedAfterRotationAndKeptOverAHalfWrittenPair(t *testing.T) {
	ca, err := kanametest.NewCA("ca")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ca.Issue("a", []string{"a.test"}, nil)
	b, _ := ca.Issue("b", []string{"b.test"}, nil)
	dir := t.TempDir()
	certFile, keyFile, _, err := a.WriteFiles(dir, ca)
	if err != nil {
		t.Fatal(err)
	}
	kp, err := tlsfile.NewKeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaf := func() []byte {
		t.Helper()
		s, err := kp.ServerCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err := kp.ClientCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(s.Certificate[0], c.Certificate[0]) {
			t.Fatal("server and client sides hold different pairs")
		}
		return s.Certificate[0]
	}
	if !bytes.Equal(leaf(), a.TLS.Certificate[0]) {
		t.Fatal("first pair is not the one on disk")
	}
	later := time.Now().Add(time.Minute)
	if _, _, _, err := b.WriteFiles(dir, ca); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certFile, later, later); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaf(), b.TLS.Certificate[0]) {
		t.Fatal("rotated pair is not picked up")
	}
	if err := os.WriteFile(certFile, a.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	evenLater := later.Add(time.Minute)
	if err := os.Chtimes(certFile, evenLater, evenLater); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaf(), b.TLS.Certificate[0]) {
		t.Fatal("a half-written pair replaced the working one")
	}
}

// Без годной пары на старте — отказ; файл центров без сертификата — отказ.
func TestRefusals(t *testing.T) {
	dir := t.TempDir()
	if _, err := tlsfile.NewKeyPair(filepath.Join(dir, "absent.crt"), filepath.Join(dir, "absent.key")); err == nil {
		t.Fatal("a missing pair was accepted at start")
	}
	empty := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsfile.CAPool(empty); err == nil || !strings.Contains(err.Error(), "holds no certificate") {
		t.Fatalf("a CA file without a certificate: %v", err)
	}
	ca, err := kanametest.NewCA("ca")
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.crt")
	if err := os.WriteFile(good, ca.PEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsfile.CAPool(good); err != nil {
		t.Fatalf("a CA file with a certificate refused: %v", err)
	}
}
