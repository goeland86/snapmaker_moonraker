package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Printer PrinterConfig `yaml:"printer"`
	Files   FilesConfig   `yaml:"files"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type PrinterConfig struct {
	IP    string `yaml:"ip"`
	Token string `yaml:"token"`
	Model string `yaml:"model"`
	// PollInterval is how often to poll printer status in seconds.
	PollInterval int `yaml:"poll_interval"`
}

type FilesConfig struct {
	// GCodeDir is the local directory for storing gcode files.
	GCodeDir string `yaml:"gcode_dir"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 7125,
		},
		Printer: PrinterConfig{
			PollInterval: 2,
			Model:        "Snapmaker J1S",
		},
		Files: FilesConfig{
			GCodeDir: "gcodes",
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Resolve relative gcode dir to absolute path.
	if !filepath.IsAbs(cfg.Files.GCodeDir) {
		dir, _ := os.Getwd()
		cfg.Files.GCodeDir = filepath.Join(dir, cfg.Files.GCodeDir)
	}

	return cfg, nil
}

func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.Server.Host, c.Server.Port)
}
