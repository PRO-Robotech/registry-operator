// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kanametest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// CA — центр сертификатов проб.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  []byte
}

// NewCA — свежий центр.
func NewCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// Pool — пул из одного центра.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.Cert)
	return p
}

// Leaf — лист центра с DNS- и URI-SAN.
type Leaf struct {
	TLS     tls.Certificate
	CertPEM []byte
	KeyPEM  []byte
}

// Issue выпускает лист.
func (c *CA) Issue(cn string, dns []string, uris []string) (*Leaf, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	var us []*url.URL
	for _, u := range uris {
		pu, err := url.Parse(u)
		if err != nil {
			return nil, err
		}
		us = append(us, pu)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dns,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		URIs:         us,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.Cert, &key.PublicKey, c.Key)
	if err != nil {
		return nil, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &Leaf{TLS: pair, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// WriteFiles кладёт лист и центр в каталог: tls.crt, tls.key, ca.crt.
func (l *Leaf) WriteFiles(dir string, ca *CA) (certFile, keyFile, caFile string, err error) {
	certFile, keyFile, caFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), filepath.Join(dir, "ca.crt")
	for _, f := range []struct {
		path string
		b    []byte
	}{{certFile, l.CertPEM}, {keyFile, l.KeyPEM}, {caFile, ca.PEM}} {
		if err = os.WriteFile(f.path, f.b, 0o600); err != nil {
			return
		}
	}
	return
}
