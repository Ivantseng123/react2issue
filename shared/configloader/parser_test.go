package configloader

import "testing"

func TestPickParser(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"cfg.yaml", false},
		{"cfg.yml", false},
		{"cfg.json", false},
		{"cfg.toml", true},
		{"cfg", true},
	}
	for _, c := range cases {
		_, err := PickParser(c.name)
		if (err != nil) != c.wantErr {
			t.Errorf("PickParser(%q) err=%v, wantErr=%v", c.name, err, c.wantErr)
		}
	}
}
