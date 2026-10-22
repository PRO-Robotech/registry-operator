// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Переменные слоя заказа платформы, у которых есть форма. Слой заказа чарта
// registry-platform переносит их в storage.endpoint и storage.region чарта
// registry-instance, а схема чарта отвергает рендер с иной формой: Addon,
// который чарт отверг бы, оператор не пишет.
const (
	// VarEndpoint — https-origin S3 бакета заказа (status.endpoint бакета).
	VarEndpoint = "endpoint"
	// VarRegion — регион S3 площадки (params.storage.region ревизии).
	VarRegion = "region"
)

// maxRegion — предел длины имени региона (схема registry-instance).
const maxRegion = 64

var (
	// originHost — DNS-имя хоста origin в нижнем регистре (схема
	// registry-instance, httpsOrigin).
	originHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// regionName — имя региона (схема registry-instance, storage.region).
	regionName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// HTTPSOrigin приводит endpoint бакета к https://<host>[:port]: хост без
// схемы получает https, схема и хост — нижний регистр, завершающий «/»
// снимается. Простой http, иная схема, путь, учётные данные, запрос,
// фрагмент, порт вне 1..65535 и хост не DNS-именем — отказ. Текст отказа
// значения не повторяет: в нём могли быть учётные данные.
func HTTPSOrigin(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("is empty: the bucket controller has not reported an endpoint")
	}
	rest := raw
	if scheme, after, ok := strings.Cut(raw, "://"); ok {
		switch strings.ToLower(scheme) {
		case "https":
			rest = after
		case "http":
			return "", errors.New("is plain http: the registry reaches its bucket only over https")
		default:
			return "", errors.New("has a scheme other than https")
		}
	}
	u, err := url.Parse("https://" + rest)
	if err != nil {
		return "", errors.New("is not an https origin https://<host>[:port]")
	}
	switch {
	case u.User != nil:
		return "", errors.New("carries credentials: an endpoint is an https origin https://<host>[:port]")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(rest, "#"):
		return "", errors.New("carries a query or a fragment: an endpoint is an https origin https://<host>[:port]")
	case u.Path != "" && u.Path != "/":
		return "", errors.New("carries a path: an endpoint is an https origin https://<host>[:port]")
	}
	host := strings.ToLower(u.Hostname())
	if !originHost.MatchString(host) || len(host) > 253 {
		return "", errors.New("has a host that is not a DNS name")
	}
	origin := "https://" + host
	if p := u.Port(); p != "" || strings.HasSuffix(u.Host, ":") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || len(p) > 5 {
			return "", errors.New("has a port outside 1..65535")
		}
		origin += ":" + p
	}
	return origin, nil
}

// checkRegion — имя региона площадки: непустое, по форме схемы чарта.
func checkRegion(region string) error {
	if region == "" || len(region) > maxRegion || !regionName.MatchString(region) {
		return errors.New("must be a non-empty region name matching " + regionName.String() +
			" of at most " + strconv.Itoa(maxRegion) + " characters: the instance chart refuses an Addon without one")
	}
	return nil
}

// variableForms — проверка формы переменных слоя заказа, у которых она
// есть: значение, отличное от своей нормальной формы, — нарушение.
var variableForms = map[string]func(string) error{
	VarEndpoint: func(v string) error {
		origin, err := HTTPSOrigin(v)
		if err != nil {
			return err
		}
		if origin != v {
			return errors.New("must be written in the normal form https://<host>[:port] (lower case, no trailing slash)")
		}
		return nil
	},
	VarRegion: checkRegion,
}

// CheckTemplateParams — параметры ревизии шаблона заказа несут то, без чего
// Addon заказа не рисуется: регион площадки params.storage.region.
func CheckTemplateParams(params map[string]any) error {
	v := &Violations{Kind: "RegistryTemplate"}
	storage, _ := params["storage"].(map[string]any)
	region, _ := storage["region"].(string)
	if err := checkRegion(region); err != nil {
		v.add("params.storage.region", "%v", err)
	}
	return v.err()
}
