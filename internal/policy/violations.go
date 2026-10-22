// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// Violation — одно нарушение политики.
type Violation struct {
	Field  string
	Reason string
}

// Violations — все нарушения разом: отказ называет каждое.
type Violations struct {
	Kind  string
	Items []Violation
}

func (v *Violations) Error() string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s policy violations:", v.Kind)
	for _, it := range v.Items {
		fmt.Fprintf(&b, " %s: %s;", it.Field, it.Reason)
	}
	return b.String()
}

func (v *Violations) add(field, format string, args ...any) {
	v.Items = append(v.Items, Violation{Field: field, Reason: fmt.Sprintf(format, args...)})
}

func (v *Violations) err() error {
	if len(v.Items) == 0 {
		return nil
	}
	sort.SliceStable(v.Items, func(i, j int) bool { return v.Items[i].Field < v.Items[j].Field })
	return v
}

// decodeStrict переносит отображение в зеркальную структуру разрешённых
// полей: поле вне зеркала — нарушение (так закрыт перечень полей вида без
// перечисления запрещённых).
func decodeStrict(kind string, m map[string]any, into any) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return &Violations{Kind: kind, Items: []Violation{{Field: "spec", Reason: err.Error()}}}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &Violations{Kind: kind, Items: []Violation{{Field: "spec", Reason: "field outside the allowed shape: " + err.Error()}}}
	}
	return nil
}

// refuseOperatorKeys — ключи, которые ставит только оператор; шаблон,
// отрисовавший любой из них, отвергнут.
func refuseOperatorKeys(kind string, rendered map[string]any, keys map[string]string) error {
	v := &Violations{Kind: kind}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if _, set := rendered[k]; set {
			v.add("spec."+k, "%s", keys[k])
		}
	}
	return v.err()
}
