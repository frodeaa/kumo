package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var jsonUnmarshalerType = reflect.TypeFor[json.Unmarshaler]()

// coerceQueryJSON rewrites body, the JSON the Query dispatcher converted from
// a form, so its values match the types of target's fields. Form values carry
// no type, so the conversion can leave "1.0" a string for a float field or
// turn "123" into a number for a string field. Numbers keep their original
// digits when they become strings. Types with their own UnmarshalJSON are left
// to it.
func coerceQueryJSON(body []byte, target reflect.Type) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("failed to decode JSON: %w", err)
	}

	coerced, err := json.Marshal(coerceValue(value, target))
	if err != nil {
		return nil, fmt.Errorf("failed to encode JSON: %w", err)
	}

	return coerced, nil
}

func coerceValue(value any, t reflect.Type) any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if reflect.PointerTo(t).Implements(jsonUnmarshalerType) {
		return value
	}

	switch t.Kind() { //nolint:exhaustive // scalar kinds are handled by coerceScalar
	case reflect.Slice:
		if items, ok := value.([]any); ok && t.Elem().Kind() != reflect.Uint8 {
			for i := range items {
				items[i] = coerceValue(items[i], t.Elem())
			}
		}
	case reflect.Map:
		if fields, ok := value.(map[string]any); ok {
			for key, field := range fields {
				fields[key] = coerceValue(field, t.Elem())
			}
		}
	case reflect.Struct:
		if fields, ok := value.(map[string]any); ok {
			coerceStructFields(fields, t)
		}
	default:
		return coerceScalar(value, t.Kind())
	}

	return value
}

// coerceScalar turns a number or boolean into the string a string field
// expects, and a numeric or boolean string into the value a number or bool
// field expects.
func coerceScalar(value any, kind reflect.Kind) any {
	s, isString := value.(string)

	switch {
	case kind == reflect.String:
		return coerceToString(value)
	case kind == reflect.Bool && isString:
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	case isNumericKind(kind) && isString:
		if _, err := strconv.ParseFloat(s, 64); err == nil {
			return json.Number(s)
		}
	}

	return value
}

func isNumericKind(kind reflect.Kind) bool {
	return (kind >= reflect.Int && kind <= reflect.Uint64) || kind == reflect.Float32 || kind == reflect.Float64
}

func coerceToString(value any) any {
	switch v := value.(type) {
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	default:
		return value
	}
}

// coerceStructFields coerces each value in fields to the type of the struct
// field it decodes into, matching names case-insensitively as encoding/json
// does.
func coerceStructFields(fields map[string]any, t reflect.Type) {
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}

		if name == "" && field.Anonymous && field.Type.Kind() == reflect.Struct {
			coerceStructFields(fields, field.Type)

			continue
		}

		if name == "" {
			name = field.Name
		}

		for key, value := range fields {
			if strings.EqualFold(key, name) {
				fields[key] = coerceValue(value, field.Type)
			}
		}
	}
}
