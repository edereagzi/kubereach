package service

// SetUpdateCheck turns the automatic update check on or off.
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
