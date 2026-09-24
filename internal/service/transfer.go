package service

import (
	"os"
	"slices"

	"github.com/goccy/go-yaml"
)

// ImportPreview is what ImportConfig would add from a file: entries whose ID is not configured yet, how many
// were skipped as duplicates, and every kubeconfig or key path in the new entries that does not exist locally.
// A file without the export schema is a kubeconfig; only Path and Kubeconfig are set then.
type ImportPreview struct {
	Path         string        `json:"path"`
	Kubeconfig   bool          `json:"kubeconfig"`
	Routes       []Route       `json:"routes"`
	Clusters     []Cluster     `json:"clusters"`
	Forwards     []PortForward `json:"forwards"`
	Duplicates   int           `json:"duplicates"`
	MissingPaths []string      `json:"missingPaths"`
}

// ExportConfig writes the configuration to path. The model holds no secrets, so neither does the file.
func (s *Service) ExportConfig(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	return saveConfig(path, cfg)
}

func (s *Service) InspectImport(path string) (ImportPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return ImportPreview{}, err
	}
	return previewImport(cfg, path)
}

func previewImport(cfg Config, path string) (ImportPreview, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ImportPreview{}, err
	}
	if !isExport(data) {
		return ImportPreview{Path: path, Kubeconfig: true}, nil
	}
	in, err := parseConfig(path, data)
	if err != nil {
		return ImportPreview{}, err
	}
	p := ImportPreview{Path: path, Routes: []Route{}, Clusters: []Cluster{}, Forwards: []PortForward{}, MissingPaths: []string{}}
	for _, r := range in.Routes {
		if _, err := findRoute(cfg, r.ID); err == nil {
			p.Duplicates++
			continue
		}
		p.Routes = append(p.Routes, r)
		for _, srv := range r.Servers {
			p.MissingPaths = noteMissing(p.MissingPaths, srv.KeyFile)
		}
	}
	for _, c := range in.Clusters {
		if _, err := findCluster(cfg, c.ID); err == nil {
			p.Duplicates++
			continue
		}
		p.Clusters = append(p.Clusters, c)
		p.MissingPaths = noteMissing(p.MissingPaths, c.Kubeconfig)
	}
	for _, f := range in.Forwards {
		if slices.ContainsFunc(cfg.Forwards, func(o PortForward) bool { return o.ID == f.ID || o.sameTarget(f) }) {
			p.Duplicates++
			continue
		}
		p.Forwards = append(p.Forwards, f)
	}
	return p, nil
}

// isExport reports whether data carries the export schema: a top-level version, which a kubeconfig never has.
func isExport(data []byte) bool {
	var probe struct {
		Version int `yaml:"version"`
	}
	return yaml.Unmarshal(data, &probe) == nil && probe.Version != 0
}

// noteMissing appends path to missing when it is set, not yet listed and absent from the local disk.
func noteMissing(missing []string, path string) []string {
	if path == "" || slices.Contains(missing, path) {
		return missing
	}
	if _, err := os.Stat(path); err != nil {
		return append(missing, path)
	}
	return missing
}

// ImportConfig adds the file's new entries. remap gives a local path for each missing one; an empty
// replacement skips every entry that references it. A Cluster whose Route is absent and a forward whose
// Cluster is absent are skipped too. Imported forwards keep their local port unless a Saved Forward holds it.
func (s *Service) ImportConfig(path string, remap map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	p, err := previewImport(cfg, path)
	if err != nil {
		return err
	}
	for _, from := range p.MissingPaths {
		to, ok := remap[from]
		if !ok {
			return userErrorf("%s does not exist", from)
		}
		if to != "" {
			fi, err := os.Stat(to)
			if err != nil {
				return err
			}
			if !fi.Mode().IsRegular() {
				return userErrorf("%s is not a file", to)
			}
		}
	}
	// resolve returns the local path for a referenced one and false when its entry is to be skipped.
	resolve := func(path string) (string, bool) {
		if !slices.Contains(p.MissingPaths, path) {
			return path, true
		}
		return remap[path], remap[path] != ""
	}
	for _, r := range p.Routes {
		kept := true
		for i := range r.Servers {
			r.Servers[i].KeyFile, kept = resolve(r.Servers[i].KeyFile)
			if !kept {
				break
			}
		}
		if kept {
			cfg.Routes = append(cfg.Routes, r)
		}
	}
	for _, c := range p.Clusters {
		if c.Kubeconfig, _ = resolve(c.Kubeconfig); c.Kubeconfig == "" {
			continue
		}
		if _, err := findRoute(cfg, c.RouteID); c.RouteID != "" && err != nil {
			continue
		}
		cfg.Clusters = append(cfg.Clusters, c)
	}
	var added []PortForward
	for _, f := range p.Forwards {
		if _, err := findCluster(cfg, f.ClusterID); err != nil {
			continue
		}
		if f.ID == "" {
			f.ID = newID()
		}
		if slices.ContainsFunc(cfg.Forwards, func(o PortForward) bool { return o.LocalPort == f.LocalPort }) {
			if f.LocalPort, err = nextFreePort(cfg); err != nil {
				return err
			}
		}
		cfg.Forwards = append(cfg.Forwards, f)
		added = append(added, f)
	}
	if err := s.saveConfig(cfg); err != nil {
		return err
	}
	for _, f := range added {
		s.applyForward(f)
	}
	return nil
}
