package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testTimeout = 10 * time.Second

// pong starts a server that answers "pong" to every request and returns its host and port.
func pong(t *testing.T) (host string, port int) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "pong") }))
	t.Cleanup(server.Close)

	address, ok := server.Listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return address.IP.String(), address.Port
}

func startFor(t *testing.T, domains ...string) *Proxy {
	t.Helper()

	proxy, err := StartProxy(t.Context(), domains)
	require.NoError(t, err)
	t.Cleanup(func() { _ = proxy.Close() })

	return proxy
}

// get sends a plain HTTP request for target through the proxy and returns the status and the body.
func get(t *testing.T, proxy *Proxy, target string) (int, string) {
	t.Helper()

	proxyURL, err := url.Parse("http://127.0.0.1:" + strconv.Itoa(proxy.Port()))
	require.NoError(t, err)

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: testTimeout}
	t.Cleanup(client.CloseIdleConnections)

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)

	response, err := client.Do(request)
	require.NoError(t, err)

	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	return response.StatusCode, string(body)
}

// connect sends CONNECT for target to the proxy and returns the connection and the answer. The answer is nil when the proxy closes the connection
// without one, which is how it refuses a host.
func connect(t *testing.T, proxy *Proxy, target string) (*bufio.Reader, *http.Response) {
	t.Helper()

	dialer := &net.Dialer{Timeout: testTimeout}
	conn, err := dialer.DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(proxy.Port()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(testTimeout)))

	_, err = io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	require.NoError(t, err)

	reader := bufio.NewReader(conn)

	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return reader, nil
	}

	return reader, response
}

func TestProxy_PlainHTTPToAListedHostGoesThroughAndAnUnlistedHostGets403(t *testing.T) {
	t.Parallel()

	host, port := pong(t)
	target := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/"

	status, body := get(t, startFor(t, host), target)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "pong", body)

	status, body = get(t, startFor(t, "example.org"), target)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Contains(t, body, "is not in the list of allowed domains")
	assert.NotContains(t, body, "pong")

	status, _ = get(t, startFor(t), target)
	assert.Equal(t, http.StatusForbidden, status, "an empty list lets nothing through")
}

func TestProxy_ConnectToAListedHostOpensATunnelAndAnUnlistedHostIsRefused(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		fmt.Fprint(conn, "welcome\n")
	}()

	target := listener.Addr().String()

	reader, response := connect(t, startFor(t, "127.0.0.1"), target)
	require.NotNil(t, response)
	require.Equal(t, http.StatusOK, response.StatusCode)

	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "welcome\n", line, "the bytes of the tunnel pass through")

	reader, response = connect(t, startFor(t, "example.org"), target)
	if response != nil {
		assert.NotEqual(t, http.StatusOK, response.StatusCode, "an unlisted host gets no tunnel")
	}

	_, err = reader.ReadString('\n')
	assert.Error(t, err, "the refused connection carries no bytes of the target")
}

func TestProxy_CloseStopsTheListener(t *testing.T) {
	t.Parallel()

	proxy, err := StartProxy(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, proxy.Close())

	_, err = (&net.Dialer{Timeout: testTimeout}).DialContext(t.Context(), "tcp", "127.0.0.1:"+strconv.Itoa(proxy.Port()))
	assert.Error(t, err)
}

func TestProxy_CloseReportsAServeFailure(t *testing.T) {
	t.Parallel()

	failure := errors.New("the listener broke")
	proxy := &Proxy{server: &http.Server{ReadHeaderTimeout: testTimeout}, done: make(chan error, 1)}
	proxy.done <- failure

	assert.ErrorIs(t, proxy.Close(), failure)
}

func TestStartProxy_AListenerThatCannotOpenIsReported(t *testing.T) {
	t.Parallel()

	failure := errors.New("no port")

	_, err := startProxy(t.Context(), nil, func(context.Context, string, string) (net.Listener, error) { return nil, failure })
	require.ErrorIs(t, err, failure)
	assert.ErrorContains(t, err, "start the sandbox proxy")
}

// unixAddress is a listener that reports an address that is not a TCP address, and keeps whether it was closed.
type unixAddress struct {
	net.Listener
	closed bool
}

func (*unixAddress) Addr() net.Addr { return &net.UnixAddr{Name: "proxy.sock", Net: "unix"} }

func (u *unixAddress) Close() error {
	u.closed = true

	return u.Listener.Close()
}

func TestStartProxy_AListenerWithoutATCPAddressIsRefusedAndClosed(t *testing.T) {
	t.Parallel()

	var odd *unixAddress

	_, err := startProxy(t.Context(), nil, func(ctx context.Context, network, address string) (net.Listener, error) {
		real, err := (&net.ListenConfig{}).Listen(ctx, network, address)
		odd = &unixAddress{Listener: real}

		return odd, err
	})
	require.ErrorContains(t, err, "no TCP address")
	assert.True(t, odd.closed, "the refused listener is closed")
}
