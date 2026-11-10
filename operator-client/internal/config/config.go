// Package config persists adbo's local settings in ~/.config/adbo/config.yaml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// Config is the on-disk file. Selected clusters are keyed by kube-context,
// then namespace, so a context or namespace switch never retargets a
// cluster picked somewhere else.
type Config struct {
	SelectedClusters map[string]map[string]string `json:"selectedClusters,omitempty"`
}

// Path is $XDG_CONFIG_HOME/adbo/config.yaml, else ~/.config/adbo/config.yaml.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "adbo", "config.yaml"), nil
}

// Load reads the config. A missing file is an empty config, not an error.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config atomically so a crash cannot leave a torn file.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// A unique temp file (created 0600) per save, so two adbo processes
	// saving at once cannot interleave writes into the same file.
	f, err := os.CreateTemp(filepath.Dir(path), "config-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SelectedCluster returns the cluster selected for kubeContext + namespace, or "".
func (c *Config) SelectedCluster(kubeContext, namespace string) string {
	return c.SelectedClusters[kubeContext][namespace]
}

// SetSelectedCluster records name for kubeContext + namespace. An empty name clears it.
func (c *Config) SetSelectedCluster(kubeContext, namespace, name string) {
	if name == "" {
		delete(c.SelectedClusters[kubeContext], namespace)
		if len(c.SelectedClusters[kubeContext]) == 0 {
			delete(c.SelectedClusters, kubeContext)
		}
		return
	}
	if c.SelectedClusters == nil {
		c.SelectedClusters = map[string]map[string]string{}
	}
	if c.SelectedClusters[kubeContext] == nil {
		c.SelectedClusters[kubeContext] = map[string]string{}
	}
	c.SelectedClusters[kubeContext][namespace] = name
}
