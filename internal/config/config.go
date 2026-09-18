package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const appName = "harness"

type Config struct {
	Port     int    `json:"port"`
	Bind     string `json:"bind"`     // "lan", "tailnet", a specific IP, or empty
	DataDir  string `json:"dataDir"`  // stores SQLite DB, TLS cert, etc.
	CertFile string `json:"certFile"` // absolute path; computed from DataDir if empty
	KeyFile  string `json:"keyFile"`  // absolute path; computed from DataDir if empty
}

func Default() *Config {
	return &Config{
		Port:    7432,
		Bind:    "lan",
		DataDir: defaultDataDir(),
	}
}

func Load() (*Config, error) {
	path := configPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := Default()
		if err := cfg.EnsureDirs(); err != nil {
			return nil, err
		}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.Port == 0 {
		cfg.Port = 7432
	}
	if cfg.Bind == "" {
		cfg.Bind = "lan"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir()
	}
	cfg.applyDerivedPaths()
	return cfg, nil
}

func (c *Config) Save() error {
	c.applyDerivedPaths()
	if err := c.EnsureDirs(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0600)
}

func (c *Config) EnsureDirs() error {
	c.applyDerivedPaths()
	for _, d := range []string{c.DataDir, filepath.Dir(c.CertFile)} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) applyDerivedPaths() {
	if c.CertFile == "" {
		c.CertFile = filepath.Join(c.DataDir, "cert.pem")
	}
	if c.KeyFile == "" {
		c.KeyFile = filepath.Join(c.DataDir, "key.pem")
	}
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, appName, "config.json")
}

func defaultDataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, appName, "data")
}
