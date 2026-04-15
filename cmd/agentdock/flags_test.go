package main

import (
	"reflect"
	"strings"
	"testing"

	"agentdock/internal/config"
)

func TestFlagToKey_ValuesMapToConfigYAMLPaths(t *testing.T) {
	valid := map[string]bool{}
	walkYAMLPaths(reflect.TypeOf(config.Config{}), "", valid)

	for flag, key := range flagToKey {
		if !valid[key] {
			t.Errorf("flagToKey[%q] = %q, but %q is not a valid yaml path in config.Config", flag, key, key)
		}
	}
}

func walkYAMLPaths(t reflect.Type, prefix string, out map[string]bool) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		out[path] = true
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			walkYAMLPaths(ft, path, out)
		}
	}
}
