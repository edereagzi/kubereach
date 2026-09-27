package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	clientauthv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// tokenEnv holds a Terminal's token in its shell's environment, and nowhere else outside Kubereach's memory.
const tokenEnv = "KUBEREACH_TOKEN"

// Credential is `kubereach credential`, the exec credential of a Terminal's kubeconfig: it hands kubectl the token
// from the Terminal's environment.
func Credential(w io.Writer) error {
	token := os.Getenv(tokenEnv)
	if token == "" {
		return errors.New(tokenEnv + " is not set: kubereach credential serves kubectl in a Terminal opened in Kubereach")
	}
	return json.NewEncoder(w).Encode(clientauthv1.ExecCredential{
		TypeMeta: metav1.TypeMeta{APIVersion: clientauthv1.SchemeGroupVersion.String(), Kind: "ExecCredential"},
		Status:   &clientauthv1.ExecCredentialStatus{Token: token},
	})
}

// kubeProxy serves one Cluster's API on 127.0.0.1 to the one Terminal holding its token, sending each request on with
// the Cluster's own client: its Route, credentials, exec plugins and OIDC refresh. It is served over TLS with a
// certificate made in memory, whose key never touches disk.
type kubeProxy struct {
	s         *Service
	clusterID string
	token     string
	url       string
	ca        []byte
	srv       *http.Server
	// stop cancels every request, upgraded ones included, which outlive the server's Close.
	stop context.CancelFunc
	// dir holds the Terminal's kubeconfig, the file KUBECONFIG points its shell to, and its shell's startup script.
	dir, kubeconfig string

	mu sync.Mutex
	// config is the Cluster's client config rp was built from, rebuilt when the Cluster's client is.
	config *rest.Config
	rp     *httputil.ReverseProxy
	rts    []http.RoundTripper
}

func (s *Service) startKubeProxy(clusterID string) (*kubeProxy, error) {
	cert, ca, err := localhostCert()
	if err != nil {
		return nil, err
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	p := &kubeProxy{s: s, clusterID: clusterID, token: rand.Text(), url: "https://" + ln.Addr().String(), ca: ca, stop: stop}
	p.srv = &http.Server{Handler: p, BaseContext: func(net.Listener) context.Context { return ctx }, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// close revokes the token: the server stops, and so does every request in flight. The kubeconfig goes with it.
func (p *kubeProxy) close() {
	p.stop()
	_ = p.srv.Close()
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeIdle()
}

// closeIdle lets go of the connections the transports keep for reuse; callers hold p.mu.
func (p *kubeProxy) closeIdle() {
	for _, rt := range p.rts {
		utilnet.CloseIdleConnectionsFor(rt)
	}
}

func (p *kubeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(p.token)) != 1 {
		writeStatus(w, http.StatusUnauthorized, metav1.StatusReasonUnauthorized, "Unauthorized")
		return
	}
	rp, err := p.reverseProxy()
	if err != nil {
		writeStatus(w, http.StatusBadGateway, metav1.StatusReasonServiceUnavailable, Describe(err).Message)
		return
	}
	rp.ServeHTTP(w, r)
}

// reverseProxy follows the Cluster's client, which is rebuilt when its definition or kubeconfig changes and is
// unavailable while its Route is down.
func (p *kubeProxy) reverseProxy() (*httputil.ReverseProxy, error) {
	k, err := p.s.clusterClient(p.clusterID)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.config == k.config {
		return p.rp, nil
	}
	target, _, err := rest.DefaultServerUrlFor(k.config)
	if err != nil {
		return nil, err
	}
	plain, err := rest.TransportFor(k.config)
	if err != nil {
		return nil, err
	}
	// Go forces HTTP/1.1 only for a WebSocket upgrade, so a SPDY one (older kubectl, client-go port-forward) needs a
	// transport that never speaks HTTP/2.
	h1 := rest.CopyConfig(k.config)
	h1.NextProtos = []string{"http/1.1"}
	upgrade, err := rest.TransportFor(h1)
	if err != nil {
		return nil, err
	}
	p.closeIdle()
	p.config, p.rts = k.config, []http.RoundTripper{plain, upgrade}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// client-go keeps an Authorization already set, which would send the Terminal's token in place of the Cluster's credentials.
			r.Out.Header.Del("Authorization")
			// A log follow gets no headers until its first line.
			r.Out = r.Out.WithContext(context.WithValue(r.Out.Context(), noHeaderTimeout{}, true))
		},
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Upgrade") != "" {
				return upgrade.RoundTrip(r)
			}
			return plain.RoundTrip(r)
		}),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeStatus(w, http.StatusBadGateway, metav1.StatusReasonServiceUnavailable, Describe(err).Message)
		},
	}
	return p.rp, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// writeStatus answers as the API server does, so kubectl shows the message.
func writeStatus(w http.ResponseWriter, code int, reason metav1.StatusReason, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Message:  msg,
		Reason:   reason,
		Code:     int32(code),
	})
}

// localhostCert is a self-signed certificate for 127.0.0.1, and its PEM, which kubectl trusts as the CA.
func localhostCert() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Kubereach Terminal"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// writeKubeconfig writes the Terminal's kubeconfig to a directory of its own, which holds no secret: the proxy's
// address and CA, the namespace, and `kubereach credential` for the token. It is removed when the proxy closes.
// ponytail: the directory of a Kubereach that did not quit cleanly stays in the temp dir; it holds nothing to steal.
func (p *kubeProxy) writeKubeconfig(name, namespace string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p.dir, err = os.MkdirTemp("", "kubereach-"); err != nil {
		return err
	}
	p.kubeconfig = filepath.Join(p.dir, "kubeconfig")
	kc := clientcmdapi.NewConfig()
	kc.Clusters[name] = &clientcmdapi.Cluster{Server: p.url, CertificateAuthorityData: p.ca}
	kc.AuthInfos[name] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion:      clientauthv1.SchemeGroupVersion.String(),
		Command:         exe,
		Args:            []string{"credential"},
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
	}}
	kc.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name, Namespace: namespace}
	kc.CurrentContext = name
	return clientcmd.WriteToFile(*kc, p.kubeconfig)
}

// terminalNamespace is the namespace a Terminal opens on: the one the Cluster is scoped to, else its context's.
func (s *Service) terminalNamespace(c Cluster) string {
	if len(c.Namespaces) == 1 {
		return c.Namespaces[0]
	}
	var kc *clientcmdapi.Config
	if c.Remote != "" {
		// Read once per Route connection; while the Route is down, kubectl's default applies.
		if data, err := s.clusterKubeconfig(c.ID); err == nil {
			kc, _ = clientcmd.Load(data)
		}
	} else {
		kc, _ = clientcmd.LoadFromFile(c.Kubeconfig)
	}
	if kc != nil && kc.Contexts[c.Context] != nil {
		return kc.Contexts[c.Context].Namespace
	}
	return ""
}
