package service

import (
	"context"
	"net"
)

// DetectKubeconfig is the command that lists the kubeconfigs an SSH server has.
var DetectKubeconfig = detectKubeconfig

// TailSize is how much of a session's latest output is kept.
const TailSize = tailSize

// ServeForwardListener runs the accept loop of forward pf over ln until ln is closed.
func (s *Service) ServeForwardListener(pf PortForward, ln net.Listener) {
	s.acceptForward(context.Background(), &forwardConn{ln: ln, status: ForwardStatus{Forward: pf}})
}
