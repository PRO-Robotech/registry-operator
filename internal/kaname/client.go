// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package kaname

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	iamv1 "github.com/PRO-Robotech/kaname/pkg/api/kaname/cloud/iam/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/registry-operator/internal/tlsfile"
)

// ClientConfig — подключение к внутреннему листенеру kaname.
type ClientConfig struct {
	// Address — host:port внутреннего gRPC.
	Address string
	// ServerName — имя сервера в его сертификате.
	ServerName string
	// CertFile и KeyFile — клиентский лист службы registry (SAN
	// spiffe://<trust-domain>/ns/<ns>/sa/kacho-registry). Файлы
	// перечитываются при смене: cert-manager ротирует лист на месте.
	CertFile string
	KeyFile  string
	// CAFile — центр, которым подписан сертификат kaname.
	CAFile string
	// Timeout — предел одного вызова.
	Timeout time.Duration
}

// Client — Registrar поверх gRPC InternalIAMService.
type Client struct {
	rpc     iamv1.InternalIAMServiceClient
	conn    *grpc.ClientConn
	timeout time.Duration
}

var _ Registrar = (*Client)(nil)

// Dial — клиент с mTLS. Соединение ленивое: недоступный kaname не мешает
// старту, отказ виден на вызове.
func Dial(cfg ClientConfig) (*Client, error) {
	if cfg.Address == "" || cfg.ServerName == "" || cfg.CertFile == "" || cfg.KeyFile == "" || cfg.CAFile == "" {
		return nil, errors.New("kaname client: address, serverName, certFile, keyFile and caFile are required")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("kaname client: timeout must be positive")
	}
	tlsCfg, err := MutualTLS(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(cfg.Address, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("kaname client: %w", err)
	}
	return &Client{rpc: iamv1.NewInternalIAMServiceClient(conn), conn: conn, timeout: cfg.Timeout}, nil
}

// NewClient — клиент поверх готового соединения (пробы).
func NewClient(conn *grpc.ClientConn, timeout time.Duration) *Client {
	return &Client{rpc: iamv1.NewInternalIAMServiceClient(conn), conn: conn, timeout: timeout}
}

// Close закрывает соединение.
func (c *Client) Close() error { return c.conn.Close() }

// Register — регистрация ресурса.
func (c *Client) Register(ctx context.Context, r Registration) error {
	if err := r.Validate(true); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.rpc.RegisterResource(ctx, RegisterRequest(r))
	return Classify(err)
}

// RegisterRequest — запрос RegisterResource регистрации.
func RegisterRequest(r Registration) *iamv1.RegisterResourceRequest {
	return &iamv1.RegisterResourceRequest{
		SubjectId:       r.Subject,
		Relation:        r.Relation,
		Object:          r.Object,
		TraceId:         r.TraceID,
		ParentProjectId: r.ParentProjectID,
		ParentAccountId: r.ParentAccountID,
		SourceVersion:   timestamppb.New(r.Version),
		ParentChain:     r.ParentChain,
	}
}

// UnregisterRequest — запрос UnregisterResource снятия.
func UnregisterRequest(r Registration) *iamv1.UnregisterResourceRequest {
	return &iamv1.UnregisterResourceRequest{
		SubjectId:       r.Subject,
		Relation:        r.Relation,
		Object:          r.Object,
		TraceId:         r.TraceID,
		ParentProjectId: r.ParentProjectID,
		ParentAccountId: r.ParentAccountID,
		SourceVersion:   timestamppb.New(r.Version),
	}
}

// Unregister — снятие ресурса.
func (c *Client) Unregister(ctx context.Context, r Registration) error {
	if err := r.Validate(false); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.rpc.UnregisterResource(ctx, UnregisterRequest(r))
	return Classify(err)
}

// Classify разводит отказ по существу и недоступность. Перечень кодов
// отказа закрыт; всё прочее — недоступность: решение о повторе остаётся за
// вызывающим, а «прочее» не превращается в окончательный отказ.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition, codes.AlreadyExists, codes.OutOfRange, codes.Unimplemented:
		return fmt.Errorf("%w: %s", ErrRefused, status.Code(err))
	default:
		return fmt.Errorf("%w: %s", ErrUnavailable, status.Code(err))
	}
}

// MutualTLS — клиентская конфигурация mTLS: лист перечитывается с диска
// при смене файла, центр доверия — только объявленный.
func MutualTLS(certFile, keyFile, caFile, serverName string) (*tls.Config, error) {
	pool, err := tlsfile.CAPool(caFile)
	if err != nil {
		return nil, err
	}
	kp, err := tlsfile.NewKeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:           tls.VersionTLS12,
		RootCAs:              pool,
		ServerName:           serverName,
		GetClientCertificate: kp.ClientCertificate,
	}, nil
}
