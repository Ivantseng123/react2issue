package configloader

import (
	"log/slog"
	"reflect"
	"strings"

	"github.com/knadh/koanf/v2"
)

// WalkYAMLPathsKeyOnly populates valid and mapKeys by walking the yaml tags
// of the given struct type. Used to validate loaded keys against the schema.
func WalkYAMLPathsKeyOnly(t reflect.Type, prefix string, out map[string]bool, mapKeys map[string]bool) {
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
		if ft.Kind() == reflect.Map {
			mapKeys[path] = true
			continue
		}
		if ft.Kind() == reflect.Struct {
			WalkYAMLPathsKeyOnly(ft, path, out, mapKeys)
		}
	}
}

// WarnUnknownKeys logs slog.Warn for any koanf key not in valid.
// mapKeys contains top-level keys whose sub-keys are dynamic (e.g. "agents").
func WarnUnknownKeys(k *koanf.Koanf, valid, mapKeys map[string]bool) {
	for _, key := range k.Keys() {
		topLevel := strings.SplitN(key, ".", 2)[0]
		if mapKeys[topLevel] {
			continue
		}
		if !valid[key] {
			slog.Warn("未知設定鍵", "phase", "失敗", "key", key)
		}
	}
}
