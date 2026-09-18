package service

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// sshHost is one concrete Host block of an OpenSSH config file.
type sshHost struct {
	alias, hostName, user, keyFile, proxyJump string
	port                                      int
}

// ImportSSHConfig turns every concrete Host entry into a Route whose SSH Servers follow its ProxyJump list.
// Entries whose chain already exists as a Route are skipped. Returns the Routes added.
func (s *Service) ImportSSHConfig(path string) ([]Route, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	hosts := parseSSHConfig(f)
	byAlias := map[string]sshHost{}
	for _, h := range hosts {
		byAlias[h.alias] = h
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return nil, err
	}
	var added []Route
	for _, h := range hosts {
		servers, err := serversFor(h, byAlias, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if slices.ContainsFunc(cfg.Routes, func(r Route) bool { return sameServers(r.Servers, servers) }) {
			continue
		}
		added = append(added, Route{ID: newID(), Name: h.alias, Servers: servers})
	}
	if len(added) == 0 {
		return nil, nil
	}
	cfg.Routes = append(cfg.Routes, added...)
	return added, saveConfig(s.configPath, cfg)
}

// sshBlock is one Host block: its patterns and the first value seen for each keyword.
type sshBlock struct {
	patterns []string
	values   map[string]string
}

// parseSSHConfig resolves every concrete Host alias the way ssh does: for each keyword the first
// value from a block whose pattern matches wins, so "Host *" defaults apply. Match blocks are ignored.
// ponytail: no Include, Match, negated patterns or %-token expansion; add a real parser when a user's config needs them.
func parseSSHConfig(r io.Reader) []sshHost {
	var blocks []*sshBlock
	var current *sshBlock
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.IndexAny(line, " \t=")
		if i < 0 {
			continue
		}
		key := strings.ToLower(line[:i])
		val := strings.Trim(strings.TrimLeft(line[i:], " \t="), `"`)
		switch key {
		case "host":
			current = &sshBlock{patterns: strings.Fields(val), values: map[string]string{}}
			blocks = append(blocks, current)
		case "match":
			current = nil
		default:
			if current != nil {
				if _, seen := current.values[key]; !seen {
					current.values[key] = val
				}
			}
		}
	}
	var hosts []sshHost
	for _, b := range blocks {
		for _, alias := range b.patterns {
			if strings.ContainsAny(alias, "*?!") || slices.ContainsFunc(hosts, func(h sshHost) bool { return h.alias == alias }) {
				continue
			}
			hosts = append(hosts, resolveHost(alias, blocks))
		}
	}
	return hosts
}

func resolveHost(alias string, blocks []*sshBlock) sshHost {
	h := sshHost{alias: alias}
	value := func(key string) string {
		for _, b := range blocks {
			if v, ok := b.values[key]; ok && slices.ContainsFunc(b.patterns, func(p string) bool { return matchPattern(p, alias) }) {
				return v
			}
		}
		return ""
	}
	h.hostName = value("hostname")
	h.user = value("user")
	h.keyFile = value("identityfile")
	h.proxyJump = value("proxyjump")
	h.port, _ = strconv.Atoi(value("port"))
	return h
}

// matchPattern implements ssh's host patterns: * and ? wildcards; negations never match.
func matchPattern(pattern, alias string) bool {
	if strings.HasPrefix(pattern, "!") {
		return false
	}
	ok, err := filepath.Match(pattern, alias)
	return err == nil && ok
}

// serversFor resolves ProxyJump recursively, outermost server first, ending with the host itself.
func serversFor(h sshHost, byAlias map[string]sshHost, depth int) ([]SSHServer, error) {
	if depth > 8 {
		return nil, fmt.Errorf("ProxyJump chain for %q is too deep or circular", h.alias)
	}
	var servers []SSHServer
	if h.proxyJump != "" && !strings.EqualFold(h.proxyJump, "none") {
		for jump := range strings.SplitSeq(h.proxyJump, ",") {
			jump = strings.TrimSpace(jump)
			via, ok := byAlias[jump]
			if !ok {
				via = literalHost(jump)
			}
			sub, err := serversFor(via, byAlias, depth+1)
			if err != nil {
				return nil, err
			}
			servers = append(servers, sub...)
		}
	}
	return append(servers, h.server()), nil
}

// literalHost parses a ProxyJump destination written as [user@]host[:port] rather than a Host alias.
func literalHost(dest string) sshHost {
	h := sshHost{alias: dest}
	if u, rest, ok := strings.Cut(dest, "@"); ok {
		h.user, dest = u, rest
	}
	if host, port, err := net.SplitHostPort(dest); err == nil {
		dest = host
		h.port, _ = strconv.Atoi(port)
	}
	h.hostName = dest
	return h
}

func (h sshHost) server() SSHServer {
	srv := SSHServer{Host: h.hostName, Port: h.port, User: h.user, Auth: AuthAgent}
	if srv.Host == "" {
		srv.Host = h.alias
	}
	if srv.Port == 0 {
		srv.Port = 22
	}
	if srv.User == "" {
		if u, err := user.Current(); err == nil {
			srv.User = u.Username
		}
	}
	if h.keyFile != "" {
		srv.Auth = AuthKeyFile
		srv.KeyFile = h.keyFile
		if rest, ok := strings.CutPrefix(h.keyFile, "~/"); ok {
			home, _ := os.UserHomeDir()
			srv.KeyFile = filepath.Join(home, rest)
		}
	}
	return srv
}

func sameServers(a, b []SSHServer) bool {
	return slices.EqualFunc(a, b, func(x, y SSHServer) bool {
		return x.Host == y.Host && x.Port == y.Port && x.User == y.User
	})
}
