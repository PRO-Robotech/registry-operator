// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package tlsfile — материал TLS с диска: пул центров доверия из файла и
// пара «сертификат — ключ», перечитываемая при ротации на месте
// (cert-manager пишет секрет в смонтированный каталог). Один дом для
// клиента kaname, эндпоинта заявок и iam-proxy.
package tlsfile

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"
)

// CAPool — пул центров из PEM-файла; файл без единого сертификата — отказ.
func CAPool(caFile string) (*x509.CertPool, error) {
	b, err := os.ReadFile(caFile) //nolint:gosec // путь — конфигурация запуска, не вход запроса.
	if err != nil {
		return nil, fmt.Errorf("read CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("CA file %s holds no certificate", caFile)
	}
	return pool, nil
}

// KeyPair — пара с диска, перечитываемая при смене времени изменения
// файла сертификата. Ротация пишет пару не атомарно: пока новая пара не
// сошлась, действует прежняя.
type KeyPair struct {
	certFile, keyFile string

	mu      sync.Mutex
	cert    *tls.Certificate
	modTime time.Time
}

// NewKeyPair — пара, прочитанная сразу: без годной пары на старте — отказ.
func NewKeyPair(certFile, keyFile string) (*KeyPair, error) {
	k := &KeyPair{certFile: certFile, keyFile: keyFile}
	if _, err := k.Get(); err != nil {
		return nil, err
	}
	return k, nil
}

// Get — действующая пара.
func (k *KeyPair) Get() (*tls.Certificate, error) {
	st, err := os.Stat(k.certFile)
	if err != nil {
		return nil, fmt.Errorf("stat certificate: %w", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cert != nil && st.ModTime().Equal(k.modTime) {
		return k.cert, nil
	}
	c, err := tls.LoadX509KeyPair(k.certFile, k.keyFile)
	if err != nil {
		if k.cert != nil {
			return k.cert, nil
		}
		return nil, fmt.Errorf("load certificate: %w", err)
	}
	k.cert, k.modTime = &c, st.ModTime()
	return k.cert, nil
}

// ServerCertificate — GetCertificate серверной конфигурации.
func (k *KeyPair) ServerCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return k.Get()
}

// ClientCertificate — GetClientCertificate клиентской конфигурации.
func (k *KeyPair) ClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return k.Get()
}
