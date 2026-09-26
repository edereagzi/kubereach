package service

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Release is a published Kubereach release newer than the running one.
type Release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// NewerRelease asks GitHub for the latest release and returns it when it is newer than current.
// It is nil for dev builds, when the update check is off, and on any failure; the request carries no identifiers.
func (s *Service) NewerRelease(current string) *Release {
	cfg, err := s.LoadConfig()
	if err != nil || cfg.SkipUpdateCheck || current == "dev" {
		return nil
	}
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(s.ReleasesURL)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	var latest struct {
		Tag string `json:"tag_name"`
		URL string `json:"html_url"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&latest) != nil {
		return nil
	}
	if slices.Compare(versionParts(latest.Tag), versionParts(current)) <= 0 {
		return nil
	}
	return &Release{Version: latest.Tag, URL: latest.URL}
}

// versionParts reads "v1.2.3" or "1.2.3" as [1 2 3]; a part that is not a number counts as 0.
// ponytail: "0.3.0-rc1" reads as 0.3.0; fine while /releases/latest never returns a prerelease.
func versionParts(v string) []int {
	var parts []int
	for p := range strings.SplitSeq(strings.TrimPrefix(v, "v"), ".") {
		n, _ := strconv.Atoi(p)
		parts = append(parts, n)
	}
	return parts
}

// SetUpdateCheck turns the launch-time update check on or off.
func (s *Service) SetUpdateCheck(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	cfg.SkipUpdateCheck = !on
	return s.saveConfig(cfg)
}
