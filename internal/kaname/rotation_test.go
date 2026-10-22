// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kaname_test

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/kaname/kanametest"
)

// Лист перечитывается с диска после ротации; пара, записанная не до
// конца, не роняет клиента — действует прежний лист.
func TestMutualTLS_LeafIsReloadedAfterRotation(t *testing.T) {
	ca, err := kanametest.NewCA("ca")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := ca.Issue("a", nil, []string{registrySAN})
	b, _ := ca.Issue("b", nil, []string{registrySAN})
	dir := t.TempDir()
	certFile, keyFile, caFile, err := a.WriteFiles(dir, ca)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := kaname.MutualTLS(certFile, keyFile, caFile, "kaname.test")
	if err != nil {
		t.Fatal(err)
	}
	leaf := func() []byte {
		c, err := cfg.GetClientCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return c.Certificate[0]
	}
	first := leaf()
	if !bytes.Equal(first, a.TLS.Certificate[0]) {
		t.Fatal("first leaf is not the one on disk")
	}
	later := time.Now().Add(time.Minute)
	if _, _, _, err := b.WriteFiles(dir, ca); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certFile, later, later); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaf(), b.TLS.Certificate[0]) {
		t.Fatal("rotated leaf is not picked up")
	}
	// Ротация на середине: сертификат новый, ключ — ещё нет.
	if err := os.WriteFile(certFile, a.CertPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	evenLater := later.Add(time.Minute)
	if err := os.Chtimes(certFile, evenLater, evenLater); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaf(), b.TLS.Certificate[0]) {
		t.Fatal("a half-written pair replaced the working leaf")
	}
}
