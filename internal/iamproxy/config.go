// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PRO-Robotech/registry-operator/internal/kaname"
	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

// Config — запуск прокси. Неполная или небезопасная конфигурация — отказ
// старта: прокси без проверки токена или с ключами издателя по http был бы
// открытой дверью в zot.
type Config struct {
	Listen       string
	HealthListen string
	Upstream     string
	RegistryID   string
	Public       bool

	JWKSURL    string
	JWKSCAFile string
	Issuer     string
	Audience   string
	TokenTypes string

	KanameAddress    string
	KanameServerName string
	KanameCAFile     string
	CheckTimeout     time.Duration
	CacheTTL         time.Duration

	EnrollURL       string
	EnrollCAFile    string
	EnrollTokenFile string
	EnrollTimeout   time.Duration

	Realm   string
	Service string
}

// MaxCacheTTL — предел срока кэша вердиктов: отзыв права чтения действует
// не позже.
const MaxCacheTTL = 10 * time.Second

// BindFlags — флаги запуска.
func (c *Config) BindFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.Listen, "listen", ":8080", "address of the registry listener")
	fs.StringVar(&c.HealthListen, "health-listen", ":8081", "address of the health and metrics listener")
	fs.StringVar(&c.Upstream, "upstream", "http://127.0.0.1:5000", "zot on the loopback of the pod")
	fs.StringVar(&c.RegistryID, "registry-id", "", "id of the registry")
	fs.BoolVar(&c.Public, "public", false, "anonymous pull is allowed")
	fs.StringVar(&c.JWKSURL, "jwks-url", "", "https URL of the issuer key set")
	fs.StringVar(&c.JWKSCAFile, "jwks-ca", "", "CA bundle the key set is served under")
	fs.StringVar(&c.Issuer, "issuer", "", "token issuer")
	fs.StringVar(&c.Audience, "audience", "", "token audience of the registry")
	fs.StringVar(&c.TokenTypes, "token-types", "at+jwt", "comma-separated accepted typ headers")
	fs.StringVar(&c.KanameAddress, "kaname-address", "", "host:port of the public gRPC of kaname")
	fs.StringVar(&c.KanameServerName, "kaname-server-name", "", "server name in the kaname certificate")
	fs.StringVar(&c.KanameCAFile, "kaname-ca", "", "CA bundle of kaname")
	fs.DurationVar(&c.CheckTimeout, "check-timeout", 2*time.Second, "deadline of one authorization check")
	fs.DurationVar(&c.CacheTTL, "cache-ttl", MaxCacheTTL, "lifetime of a cached read verdict (at most 10s)")
	fs.StringVar(&c.EnrollURL, "enroll-url", "", "https URL of the registry-operator enrollment endpoint")
	fs.StringVar(&c.EnrollCAFile, "enroll-ca", "", "CA bundle of the enrollment endpoint")
	fs.StringVar(&c.EnrollTokenFile, "enroll-token-file", "", "projected token of the pod account with the operator audience")
	fs.DurationVar(&c.EnrollTimeout, "enroll-timeout", 15*time.Second, "deadline of one enrollment")
	fs.StringVar(&c.Realm, "realm", "", "token realm announced to clients")
	fs.StringVar(&c.Service, "service", "", "token service announced to clients")
}

// Validate — fail-closed проверка конфигурации.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !kaname.ValidID(c.RegistryID) {
		add("registry-id is required")
	}
	if u, err := url.Parse(c.JWKSURL); err != nil || u.Scheme != "https" || u.Host == "" {
		add("jwks-url must be an https URL")
	}
	if c.JWKSCAFile == "" {
		add("jwks-ca is required: the key set is trusted only under a declared CA")
	}
	if c.Issuer == "" || c.Audience == "" {
		add("issuer and audience are required")
	}
	if strings.TrimSpace(c.TokenTypes) == "" {
		add("token-types must not be empty")
	}
	if c.KanameAddress == "" || c.KanameServerName == "" || c.KanameCAFile == "" {
		add("kaname-address, kaname-server-name and kaname-ca are required")
	}
	if c.CheckTimeout <= 0 || c.CheckTimeout > 30*time.Second {
		add("check-timeout must be within (0, 30s]")
	}
	if c.CacheTTL < 0 || c.CacheTTL > MaxCacheTTL {
		add("cache-ttl must be within [0, %s]", MaxCacheTTL)
	}
	if u, err := url.Parse(c.EnrollURL); err != nil || u.Scheme != "https" || u.Host == "" {
		add("enroll-url must be an https URL")
	}
	if c.EnrollCAFile == "" || c.EnrollTokenFile == "" {
		add("enroll-ca and enroll-token-file are required")
	}
	if c.EnrollTimeout <= 0 || c.EnrollTimeout > time.Minute {
		add("enroll-timeout must be within (0, 1m]")
	}
	if c.Realm == "" || c.Service == "" {
		add("realm and service are required")
	}
	return errors.Join(errs...)
}

// Types — допустимые значения typ.
func (c *Config) Types() []string {
	var out []string
	for _, t := range strings.Split(c.TokenTypes, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// TLSClient — HTTP-клиент, доверяющий только объявленному центру.
func TLSClient(caFile string, timeout time.Duration) (*http.Client, error) {
	pool, err := tlsfile.CAPool(caFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool},
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     90 * time.Second,
		},
	}, nil
}
