// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package iamproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/PRO-Robotech/registry-operator/internal/enroll/api"
)

// Исходы заявки на новый репозиторий, которые различает прокси.
var (
	// ErrEnrollConflict — репозиторий уже записан (другим субъектом или
	// вне окна заявившего): права создания мало, нужна запись в него.
	ErrEnrollConflict = errors.New("repository is already registered")
	// ErrEnrollLimited — предел описи или темп заявок регистри.
	ErrEnrollLimited = errors.New("repository enrollment is limited")
	// ErrEnrollClosed — регистри снимается.
	ErrEnrollClosed = errors.New("registry is being removed")
	// ErrEnrollUnavailable — решения нет: оператор или IAM не ответили.
	ErrEnrollUnavailable = errors.New("repository enrollment is unavailable")
)

// Enroller — заявка на регистрацию нового репозитория.
type Enroller interface {
	Enroll(ctx context.Context, repository, subject string) error
}

// OperatorEnroller — эндпоинт заявок registry-operator. Вызывающий —
// учётка пода zot-<id>: projected-токен с аудиторией оператора читается с
// диска на каждый вызов (kubelet его ротирует).
type OperatorEnroller struct {
	// URL — https://<сервис оператора>:<порт>.
	URL        string
	RegistryID string
	TokenFile  string
	HTTPClient *http.Client
}

// NewOperatorEnroller — клиент заявок; только https.
func NewOperatorEnroller(base, registryID, tokenFile string, hc *http.Client) (*OperatorEnroller, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("enrollment url %q must be https://host[:port]", base)
	}
	if tokenFile == "" || hc == nil {
		return nil, errors.New("enrollment needs a token file and an http client")
	}
	return &OperatorEnroller{URL: strings.TrimSuffix(base, "/"), RegistryID: registryID, TokenFile: tokenFile, HTTPClient: hc}, nil
}

// Enroll — заявка; nil — репозиторий записан и субъект вправе писать в
// него как заявивший.
func (e *OperatorEnroller) Enroll(ctx context.Context, repository, subject string) error {
	token, err := os.ReadFile(e.TokenFile)
	if err != nil {
		return fmt.Errorf("%w: read token: %v", ErrEnrollUnavailable, err)
	}
	body, err := json.Marshal(api.Request{Repository: repository, Subject: subject})
	if err != nil {
		return err
	}
	path := strings.Replace(api.Path, "{id}", url.PathEscape(e.RegistryID), 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEnrollUnavailable, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrEnrollConflict
	case http.StatusTooManyRequests:
		return ErrEnrollLimited
	case http.StatusGone:
		return ErrEnrollClosed
	default:
		return fmt.Errorf("%w: status %d", ErrEnrollUnavailable, resp.StatusCode)
	}
}
