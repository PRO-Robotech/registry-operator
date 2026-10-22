// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

// Package render — закрытый рендер observed-шаблона: text/template с
// закрытым перечнем функций, AST-гейтом, закрытым контекстом, пределом
// вывода и строгим разбором YAML. Шаблон пишет платформа, но данные в нём —
// поля заказа арендатора и статусы чужих контроллеров; гейт держит, чтобы
// эти данные попадали в вывод только экранированными (toJson).
package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/template"

	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"
)

// MaxOutputBytes — предел вывода шаблона: объект целиком обязан пройти
// предел etcd с запасом.
const MaxOutputBytes = 256 << 10

// Registry — поля заказа, доступные шаблону. Методов у типа нет намеренно:
// шаблон не вправе вызвать ничего, кроме закрытого перечня функций.
type Registry struct {
	Name           string
	Namespace      string
	UID            string
	DeletionPolicy string
	SyncEpoch      int64
}

// TemplateInfo — ревизия шаблона заказа.
type TemplateInfo struct {
	Name    string
	Version string
}

// Child — готовый дочерний ресурс предыдущего шага: имя, namespace и его
// status целиком (пути и имена, которые вывел его контроллер).
type Child struct {
	Name      string
	Namespace string
	Status    map[string]any
}

// Context — закрытый контекст шаблона.
type Context struct {
	Registry Registry
	ID       string
	Login    string
	Params   map[string]any
	Template TemplateInfo
	// Status — готовые ресурсы предыдущих шагов по ключу вида
	// (S3BucketClaim, IamServiceAccount, VaultClaim, IamAccessBinding,
	// Addon). Обращение к не готовому — отказ рендера (missingkey=error).
	Status map[string]Child
}

// Template — разобранный и прошедший гейт шаблон.
type Template struct {
	t *template.Template
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"toJson": func(v any) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("toJson: %w", err)
			}
			return string(b), nil
		},
		"toYaml": func(v any) (string, error) {
			b, err := yaml.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("toYaml: %w", err)
			}
			return strings.TrimSuffix(string(b), "\n"), nil
		},
		"nindent": func(n int, s string) (string, error) {
			if n < 0 || n > 64 {
				return "", fmt.Errorf("nindent: width %d out of range", n)
			}
			pad := strings.Repeat(" ", n)
			return "\n" + pad + strings.ReplaceAll(s, "\n", "\n"+pad), nil
		},
	}
}

// Compile разбирает шаблон и прогоняет AST-гейт.
func Compile(name, src string) (*Template, error) {
	t, err := template.New(name).Option("missingkey=error").Funcs(funcMap()).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// define создаёт вторые шаблоны в наборе; запрещено и это.
	if n := len(t.Templates()); n != 1 {
		return nil, &GateError{Reason: fmt.Sprintf("template defines %d templates; define is forbidden", n-1)}
	}
	if err := gate(t.Tree); err != nil {
		return nil, err
	}
	return &Template{t: t}, nil
}

// ErrOutputTooLarge — вывод шаблона превысил предел.
var ErrOutputTooLarge = errors.New("rendered output exceeds the size limit")

type limitWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		return 0, ErrOutputTooLarge
	}
	return w.buf.Write(p)
}

// Execute исполняет шаблон и строго разбирает вывод в отображение:
// дубликаты ключей и не-отображение на верхнем уровне — отказ.
func (t *Template) Execute(ctx Context) (map[string]any, error) {
	w := &limitWriter{max: MaxOutputBytes}
	if err := t.t.Execute(w, ctx); err != nil {
		if errors.Is(err, ErrOutputTooLarge) {
			return nil, ErrOutputTooLarge
		}
		return nil, fmt.Errorf("execute: %w", err)
	}
	return StrictYAMLMap(w.buf.Bytes())
}

// StrictYAMLMap разбирает YAML строго: дубликаты ключей — отказ, на верхнем
// уровне — только отображение. Числа — int64 либо float64, как у
// unstructured.
func StrictYAMLMap(b []byte) (map[string]any, error) {
	j, err := yaml.YAMLToJSONStrict(b)
	if err != nil {
		return nil, fmt.Errorf("strict yaml: %w", err)
	}
	trimmed := bytes.TrimSpace(j)
	if bytes.Equal(trimmed, []byte("null")) {
		return map[string]any{}, nil
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("yaml document must be a mapping")
	}
	var out map[string]any
	if err := utiljson.Unmarshal(j, &out); err != nil {
		return nil, fmt.Errorf("yaml document must be a mapping: %w", err)
	}
	return out, nil
}
