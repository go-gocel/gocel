package workflow

import (
	"encoding/json"
	"fmt"
)

// Schema is the JSON-schema subset DSH allows for structured child results:
// type / properties / required / additionalProperties / items / enum /
// const / oneOf ONLY. No pattern, no format, no numeric bounds — anything
// else is rejected at parse time (CodeUnsupportedSchema, fatal).
//
// Schema 是 DSH 允许的结构化子结果 JSON-schema 子集：仅 type / properties /
// required / additionalProperties / items / enum / const / oneOf。
// 禁止 pattern、format、数值边界——其余关键字在解析期即拒绝
// （CodeUnsupportedSchema，致命）。
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	Const                any                `json:"const,omitempty"`
	OneOf                []*Schema          `json:"oneOf,omitempty"`
}

// schemaKeywords is the allowlist of the DSH JSON-schema subset.
var schemaKeywords = map[string]bool{
	"type": true, "properties": true, "required": true,
	"additionalProperties": true, "items": true, "enum": true,
	"const": true, "oneOf": true,
}

// checkSchemaKeywords rejects keywords outside the subset by walking the
// RAW schema document (a typed decode would silently drop unknown
// keywords before they could be rejected). It recurses into properties
// values, items, and oneOf alternatives.
//
// checkSchemaKeywords 遍历原始 schema 文档拒绝子集外关键字（类型化解码会
// 在拒绝前静默丢弃未知关键字）。递归进入 properties 值、items 与 oneOf
// 分支。
func checkSchemaKeywords(raw []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	for k, v := range m {
		if !schemaKeywords[k] {
			return fmt.Errorf("keyword %q is outside the supported subset", k)
		}
		switch k {
		case "properties":
			var props map[string]json.RawMessage
			if err := json.Unmarshal(v, &props); err != nil {
				return err
			}
			for _, sub := range props {
				if err := checkSchemaKeywords(sub); err != nil {
					return err
				}
			}
		case "items":
			if err := checkSchemaKeywords(v); err != nil {
				return err
			}
		case "oneOf":
			var alts []json.RawMessage
			if err := json.Unmarshal(v, &alts); err != nil {
				return err
			}
			for _, alt := range alts {
				if err := checkSchemaKeywords(alt); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validate checks a decoded JSON value against the schema subset.
func (s *Schema) validate(v any) error {
	if s == nil {
		return nil
	}
	if s.Type != "" && !typeMatches(s.Type, v) {
		return fmt.Errorf("value %v is not of type %q", v, s.Type)
	}
	if s.Enum != nil && !enumContains(s.Enum, v) {
		return fmt.Errorf("value %v is not in enum", v)
	}
	if s.Const != nil && !jsonEqual(s.Const, v) {
		return fmt.Errorf("value %v does not equal const %v", v, s.Const)
	}
	if len(s.OneOf) > 0 {
		matched := false
		for _, alt := range s.OneOf {
			if alt.validate(v) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value %v matches none of oneOf", v)
		}
	}
	switch obj := v.(type) {
	case map[string]any:
		if s.Properties == nil && len(s.Required) == 0 && s.AdditionalProperties == nil {
			return nil
		}
		extra := map[string]bool{}
		for k := range obj {
			extra[k] = true
		}
		for name, sub := range s.Properties {
			delete(extra, name)
			val, ok := obj[name]
			if !ok {
				continue // presence is enforced by Required below
			}
			if err := sub.validate(val); err != nil {
				return fmt.Errorf("property %q: %v", name, err)
			}
		}
		for _, name := range s.Required {
			if _, ok := obj[name]; !ok {
				return fmt.Errorf("required property %q missing", name)
			}
		}
		if s.AdditionalProperties != nil && !*s.AdditionalProperties && len(extra) > 0 {
			for k := range extra {
				return fmt.Errorf("additional property %q not allowed", k)
			}
		}
	case []any:
		if s.Items != nil {
			for i, item := range obj {
				if err := s.Items.validate(item); err != nil {
					return fmt.Errorf("item %d: %v", i, err)
				}
			}
		}
	}
	return nil
}

func typeMatches(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		switch v.(type) {
		case float64, json.Number:
			return true
		}
		return false
	case "integer":
		n, ok := v.(float64)
		return ok && n == float64(int64(n))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return false
}

func enumContains(enum []any, v any) bool {
	for _, e := range enum {
		if jsonEqual(e, v) {
			return true
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	aj, err1 := json.Marshal(a)
	bj, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(aj) == string(bj)
}
