package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/elazarl/goproxy"
)

// proxyTimeout bounds how long a client may take to send its request headers, and how long the proxy waits to open a tunnel.
const proxyTimeout = 10 * time.Second

// Proxy is the allow-list forward proxy that every enforcer shares. It listens on 127.0.0.1 and serves plain HTTP requests and CONNECT tunnels.
// It does not open the encrypted traffic of a tunnel. A request for a host outside the list gets status 403.
type Proxy struct {
	port   int
	server *http.Server
	done   chan error
}

// StartProxy starts a proxy that lets through only the listed domains. A domain is a host name, or *.name for every subdomain of name but not name itself.
// The context bounds only the start of the listener.
func StartProxy(ctx context.Context, domains []string) (*Proxy, error) {
	return startProxy(ctx, domains, (&net.ListenConfig{}).Listen)
}

// startProxy is StartProxy with the function that opens the listener, so a test can make it fail.
func startProxy(ctx context.Context, domains []string, listen func(ctx context.Context, network, address string) (net.Listener, error)) (*Proxy, error) {
	listener, err := listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start the sandbox proxy: %w", err)
	}

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, errors.Join(errors.New("start the sandbox proxy: the listener has no TCP address"), listener.Close())
	}

	p := &Proxy{port: addr.Port, server: &http.Server{Handler: newHandler(domains), ReadHeaderTimeout: proxyTimeout}, done: make(chan error, 1)}

	go func() { p.done <- p.server.Serve(listener) }()

	return p, nil
}

// Port returns the port that the proxy listens on.
func (p *Proxy) Port() int { return p.port }

// Close stops the proxy and the tunnels that it holds, and waits until it has stopped.
func (p *Proxy) Close() error {
	closeErr := p.server.Close()

	if serveErr := <-p.done; !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.Join(closeErr, serveErr)
	}

	return closeErr
}

func newHandler(domains []string) *goproxy.ProxyHttpServer {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Logger = log.New(io.Discard, "", 0)
	// The proxy variables of the environment of beehave must not stand between the sandbox and the host: the list decides.
	proxy.Tr.Proxy = nil
	proxy.ConnectDial = (&net.Dialer{Timeout: proxyTimeout}).Dial

	proxy.OnRequest().HandleConnectFunc(func(host string, _ *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if !allowed(domains, host) {
			return goproxy.RejectConnect, host
		}

		return &goproxy.ConnectAction{Action: goproxy.ConnectAccept}, host
	})

	proxy.OnRequest().DoFunc(func(req *http.Request, _ *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		return req, refuseUnlisted(domains, req)
	})

	return proxy
}

// refuseUnlisted returns the 403 answer for a request to a host outside the list, and nil for a listed host.
func refuseUnlisted(domains []string, req *http.Request) *http.Response {
	if allowed(domains, req.URL.Host) {
		return nil
	}

	return goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusForbidden, "beehave sandbox: the host "+req.URL.Hostname()+" is not in the list of allowed domains\n")
}

// allowed reports whether the host of a request (with or without a port) matches a listed domain.
func allowed(domains []string, hostPort string) bool {
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}

	host = strings.ToLower(strings.TrimSuffix(host, "."))

	for _, domain := range domains {
		if matches(host, strings.ToLower(domain)) {
			return true
		}
	}

	return false
}

// matches reports whether host is the domain, or, for *.name, a subdomain of name.
func matches(host, domain string) bool {
	if suffix, ok := strings.CutPrefix(domain, "*"); ok {
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}

	return host == domain
}
