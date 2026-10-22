// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package config — конфигурация оператора (config.yaml): политика площадки,
// сверка digest чарта, темп сверки и восстановления синхронизации. Разбор
// строгий, неполная конфигурация — отказ старта.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/PRO-Robotech/registry-operator/internal/policy"
)

// ChartVerification — сверка версии чарта с пином digest.
type ChartVerification struct {
	// Timeout — предел одного запроса к реестру.
	Timeout metav1.Duration `json:"timeout"`
	// CacheTTL — сколько помнится успешная сверка.
	CacheTTL metav1.Duration `json:"cacheTTL"`
	// PlainHTTPHosts — реестры внутри кластера без TLS (host:port).
	PlainHTTPHosts []string `json:"plainHTTPHosts,omitempty"`
}

// Intervals — темп сверки.
type Intervals struct {
	// NotReady — повтор, пока заказ не готов.
	NotReady metav1.Duration `json:"notReady"`
	// Ready — сверка готового заказа (дрейф дочерних ресурсов).
	Ready metav1.Duration `json:"ready"`
	// Refused — повтор отвергнутого заказа (политика, рендер, чужой объект):
	// вход мог измениться без события.
	Refused metav1.Duration `json:"refused"`
	// DeletionTimeout — после него удаление получает DeletionStalled;
	// работа продолжается.
	DeletionTimeout metav1.Duration `json:"deletionTimeout"`
}

// SyncRecovery — продвижение эпохи синхронизации рабочей нагрузки после
// исчерпанного повтора Argo CD.
type SyncRecovery struct {
	// InitialDelay — пауза от первой замеченной застрявшей синхронизации до
	// первого продвижения.
	InitialDelay metav1.Duration `json:"initialDelay"`
	// MaxDelay — потолок паузы; пауза удваивается с каждым продвижением.
	MaxDelay metav1.Duration `json:"maxDelay"`
}

// Kaname — внутренний листенер kaname, куда оператор как служба registry
// пишет ресурсы регистри.
type Kaname struct {
	// Address — host:port внутреннего gRPC (:9091).
	Address string `json:"address"`
	// ServerName — имя сервера в сертификате kaname.
	ServerName string `json:"serverName"`
	// CertFile и KeyFile — клиентский лист службы registry.
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
	// CAFile — центр, которым подписан сертификат kaname.
	CAFile string `json:"caFile"`
	// Timeout — предел одного вызова.
	Timeout metav1.Duration `json:"timeout"`
}

// Enrollment — эндпоинт заявок рабочей нагрузки zot на регистрацию
// репозиториев.
type Enrollment struct {
	// Listen — адрес листенера (TLS).
	Listen string `json:"listen"`
	// CertFile и KeyFile — серверный сертификат эндпоинта.
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
	// Audience — аудитория projected-токена учётки zot, которую принимает
	// эндпоинт: токен другой аудитории (того же пода, для Vault) — отказ.
	Audience string `json:"audience"`
	// MaxRepositories — предел описи репозиториев одного регистри.
	MaxRepositories int `json:"maxRepositories"`
	// RatePerMinute и Burst — темп заявок одного регистри.
	RatePerMinute int `json:"ratePerMinute"`
	Burst         int `json:"burst"`
	// EnrollerGrace — сколько субъект, заведший репозиторий, вправе писать в
	// него по праву создания, пока IAM не материализовал права потомков.
	EnrollerGrace metav1.Duration `json:"enrollerGrace"`
	// RequestTimeout — предел обработки одной заявки.
	RequestTimeout metav1.Duration `json:"requestTimeout"`
}

// Config — конфигурация оператора.
type Config struct {
	Policy            policy.Config     `json:"policy"`
	ChartVerification ChartVerification `json:"chartVerification"`
	Kaname            Kaname            `json:"kaname"`
	Enrollment        Enrollment        `json:"enrollment"`
	Intervals         Intervals         `json:"intervals"`
	SyncRecovery      SyncRecovery      `json:"syncRecovery"`
}

var (
	hostPort     = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	hostWithPort = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?:[0-9]{1,5}$`)
	listenAddr   = regexp.MustCompile(`^([a-z0-9.-]*):[0-9]{1,5}$`)
	dnsName      = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
)

// Load читает и проверяет конфигурацию.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) //nolint:gosec // путь — флаг запуска оператора, не вход запроса.
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(b)
}

// Parse разбирает конфигурацию строго и проверяет её.
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &c, nil
}

func within(d metav1.Duration, lo, hi time.Duration) bool {
	return d.Duration >= lo && d.Duration <= hi
}

// Validate — fail-closed проверка конфигурации.
func (c *Config) Validate() error {
	var errs []error
	if err := c.Policy.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("policy: %w", err))
	}
	cv := c.ChartVerification
	if !within(cv.Timeout, time.Second, time.Minute) {
		errs = append(errs, errors.New("chartVerification.timeout must be within [1s, 1m]"))
	}
	if !within(cv.CacheTTL, time.Minute, 24*time.Hour) {
		errs = append(errs, errors.New("chartVerification.cacheTTL must be within [1m, 24h]"))
	}
	for _, h := range cv.PlainHTTPHosts {
		if !hostPort.MatchString(h) {
			errs = append(errs, fmt.Errorf("chartVerification.plainHTTPHosts: %q is not host[:port]", h))
		}
	}
	k := c.Kaname
	if !hostWithPort.MatchString(k.Address) {
		errs = append(errs, errors.New("kaname.address must be host:port of the internal listener"))
	}
	if !dnsName.MatchString(k.ServerName) {
		errs = append(errs, errors.New("kaname.serverName is required"))
	}
	if k.CertFile == "" || k.KeyFile == "" || k.CAFile == "" {
		errs = append(errs, errors.New("kaname.certFile, kaname.keyFile and kaname.caFile are required: the internal listener accepts only mutual TLS"))
	}
	if !within(k.Timeout, 100*time.Millisecond, 30*time.Second) {
		errs = append(errs, errors.New("kaname.timeout must be within [100ms, 30s]"))
	}
	e := c.Enrollment
	if !listenAddr.MatchString(e.Listen) {
		errs = append(errs, errors.New("enrollment.listen must be [host]:port"))
	}
	if e.CertFile == "" || e.KeyFile == "" {
		errs = append(errs, errors.New("enrollment.certFile and enrollment.keyFile are required: the bearer token of the workload travels only over TLS"))
	}
	if !dnsName.MatchString(e.Audience) {
		errs = append(errs, errors.New("enrollment.audience is required"))
	}
	if e.MaxRepositories < 1 || e.MaxRepositories > 1024 {
		errs = append(errs, errors.New("enrollment.maxRepositories must be within [1, 1024]"))
	}
	if e.RatePerMinute < 1 || e.RatePerMinute > 600 {
		errs = append(errs, errors.New("enrollment.ratePerMinute must be within [1, 600]"))
	}
	if e.Burst < 1 || e.Burst > 100 {
		errs = append(errs, errors.New("enrollment.burst must be within [1, 100]"))
	}
	if !within(e.EnrollerGrace, time.Minute, time.Hour) {
		errs = append(errs, errors.New("enrollment.enrollerGrace must be within [1m, 1h]"))
	}
	if !within(e.RequestTimeout, time.Second, time.Minute) {
		errs = append(errs, errors.New("enrollment.requestTimeout must be within [1s, 1m]"))
	}
	iv := c.Intervals
	if !within(iv.NotReady, time.Second, 10*time.Minute) {
		errs = append(errs, errors.New("intervals.notReady must be within [1s, 10m]"))
	}
	if !within(iv.Ready, iv.NotReady.Duration, 24*time.Hour) {
		errs = append(errs, errors.New("intervals.ready must be within [intervals.notReady, 24h]"))
	}
	if !within(iv.Refused, iv.NotReady.Duration, 24*time.Hour) {
		errs = append(errs, errors.New("intervals.refused must be within [intervals.notReady, 24h]"))
	}
	if !within(iv.DeletionTimeout, time.Minute, 24*time.Hour) {
		errs = append(errs, errors.New("intervals.deletionTimeout must be within [1m, 24h]"))
	}
	sr := c.SyncRecovery
	if !within(sr.InitialDelay, time.Second, time.Hour) {
		errs = append(errs, errors.New("syncRecovery.initialDelay must be within [1s, 1h]"))
	}
	if !within(sr.MaxDelay, sr.InitialDelay.Duration, 24*time.Hour) {
		errs = append(errs, errors.New("syncRecovery.maxDelay must be within [syncRecovery.initialDelay, 24h]"))
	}
	return errors.Join(errs...)
}
