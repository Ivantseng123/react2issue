package skill

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type SkillsFileConfig struct {
	Skills map[string]*SkillConfig `yaml:"skills"`
	Cache  CacheConfig             `yaml:"cache"`
}

type SkillConfig struct {
	Type    string        `yaml:"type"`
	Path    string        `yaml:"path"`
	Package string        `yaml:"package"`
	Version string        `yaml:"version"`
	Timeout time.Duration `yaml:"timeout"`
}

type CacheConfig struct {
	TTL time.Duration `yaml:"ttl"`
}

func LoadSkillsConfig(path string) (*SkillsFileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg SkillsFileConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	applySkillsDefaults(&cfg)
	return &cfg, nil
}

func applySkillsDefaults(cfg *SkillsFileConfig) {
	for _, s := range cfg.Skills {
		if s.Type == "npx" {
			if s.Version == "" {
				s.Version = "latest"
			}
			if s.Timeout <= 0 {
				s.Timeout = 30 * time.Second
			}
		}
	}
	if cfg.Cache.TTL <= 0 {
		cfg.Cache.TTL = 5 * time.Minute
	}
}
