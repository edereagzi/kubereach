package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
)

const sshConfig = `
# personal
Host bastion
    HostName bastion.example.com
    User ops
    Port 2222
    IdentityFile ~/.ssh/id_bastion

Host inner
  HostName 10.0.0.5
  ProxyJump bastion

Host deep
	HostName=10.0.0.9
	User=root
	ProxyJump inner

Host external
    HostName ext.example.com
    ProxyJump jump@gw.example.com:2200

Host plain

Host *.internal *
    User fallback
    Port 2200

Match host something
    User matched
`

func TestImportSSHConfig_MapsHostsAndProxyJumpChains(t *testing.T) {
	svc, _ := newService(t)
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(sshConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()

	added, err := svc.ImportSSHConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	first := service.SSHServer{Host: "bastion.example.com", Port: 2222, User: "ops", Auth: service.AuthKeyFile, KeyFile: filepath.Join(home, ".ssh", "id_bastion")}
	inner := service.SSHServer{Host: "10.0.0.5", Port: 2200, User: "fallback", Auth: service.AuthAgent}
	want := []service.Route{
		{Name: "bastion", Servers: []service.SSHServer{first}},
		{Name: "inner", Servers: []service.SSHServer{first, inner}},
		{Name: "deep", Servers: []service.SSHServer{first, inner, {Host: "10.0.0.9", Port: 2200, User: "root", Auth: service.AuthAgent}}},
		{Name: "external", Servers: []service.SSHServer{
			{Host: "gw.example.com", Port: 2200, User: "jump", Auth: service.AuthAgent},
			{Host: "ext.example.com", Port: 2200, User: "fallback", Auth: service.AuthAgent},
		}},
		{Name: "plain", Servers: []service.SSHServer{{Host: "plain", Port: 2200, User: "fallback", Auth: service.AuthAgent}}},
	}
	ignoreID := cmp.FilterPath(func(p cmp.Path) bool { return p.Last().String() == ".ID" }, cmp.Ignore())
	if diff := cmp.Diff(want, added, ignoreID); diff != "" {
		t.Errorf("routes mismatch (-want +got):\n%s", diff)
	}
	for _, r := range added {
		if r.ID == "" {
			t.Errorf("route %s has no ID", r.Name)
		}
	}

	again, err := svc.ImportSSHConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("second import duplicated %d routes", len(again))
	}
	cfg, err := svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != len(want) {
		t.Errorf("config has %d routes, want %d", len(cfg.Routes), len(want))
	}
}
