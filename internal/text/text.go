package text

import (
	"fmt"
	"reflect"
	"strings"
)

func Marshal(v any) (string, error) {
	return marshal(v, 0)
}

func marshal(v any, level int) (string, error) {
	rv := reflect.ValueOf(v)
	rt := reflect.TypeOf(v)
	if rt.Kind() == reflect.Pointer {
		rv = rv.Elem()
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return "", fmt.Errorf("structs are only supported")
	}

	var b strings.Builder
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("text")
		if tag == "-" {
			continue
		}
		if tag == "" {
			return "", fmt.Errorf("field %s missing required plain tag", field.Name)
		}
		val := rv.Field(i).Interface()
		if field.Type.Kind() == reflect.Struct {
			s, err := marshal(val, level+1)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "%s:\n%s", tag, s)
		} else {
			indent := strings.Repeat("  ", level)
			fmt.Fprintf(&b, "%s%s: %v\n", indent, tag, val)
		}
	}
	return b.String(), nil
}
