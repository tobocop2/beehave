// Package contract is the test suite that every sandbox enforcer must pass. A backend runs it from its own test with Run.
// The suite runs real commands (sh, cat, curl) and local test servers, so it needs a machine where the enforcer can run, and no internet.
// It uses the testing package only, so no production build links a test library.
package contract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tobocop2/obee/internal/sandbox"
)

const (
	commandTimeout = time.Minute
	certLifetime   = time.Hour
	secretMode     = 0o600
	exitSeven      = 7
	exitNotFound   = 127
	// commandPath is the search path of a suite command. The base plan reads /usr and /bin, so these folders are the ones a command can use.
	commandPath = "/usr/local/bin:/usr/bin:/bin"
)

// reporter is the part of *testing.T that the checks use. The suite tests itself against a recording reporter, to prove that a check fails on an enforcer that
// does not confine.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatal(args ...any)
	TempDir() string
	Cleanup(func())
}

// Run proves the contract of one enforcer: a read under home is refused, a write outside the allowed paths is refused, an unlisted domain is refused,
// a listed domain works, a direct connection that skips the proxy is refused, a connection to a unix socket outside the plan is refused (a pathname socket,
// and on Linux an abstract socket), a write folder is writable whole (a new top-level file and folder can be made and removed), and the exit code passes through.
func Run(t *testing.T, e sandbox.Enforcer) {
	t.Helper()

	requireRunnable(t, e)

	t.Run("a read under home is refused", func(t *testing.T) { readUnderHome(t, e) })
	t.Run("a write outside the allowed paths is refused", func(t *testing.T) { writeOutside(t, e) })
	t.Run("the exit code passes through", func(t *testing.T) { exitCode(t, e) })
	t.Run("a listed domain works and an unlisted domain is refused", func(t *testing.T) { domains(t, e) })
	t.Run("a connection that skips the proxy is refused", func(t *testing.T) { direct(t, e) })
	t.Run("a plan with no network cannot connect", func(t *testing.T) { noNetwork(t, e) })
	t.Run("a unix socket outside the plan is refused", func(t *testing.T) { unixSocket(t, e) })
	t.Run("a write folder is writable whole", func(t *testing.T) { wholeFolder(t, e) })
}

// requireRunnable stops the suite when the enforcer cannot run here: a suite that ran nothing would pass for the wrong reason.
func requireRunnable(r reporter, e sandbox.Enforcer) {
	r.Helper()

	if err := e.Check(); err != nil {
		r.Fatal("the contract suite needs an enforcer that can run here: ", err)
	}
}

// result is what a command printed and its exit code.
type result struct {
	out  string
	code int
}

// expect fails the test, and goes on, when ok is false.
func expect(r reporter, ok bool, format string, args ...any) {
	r.Helper()

	if !ok {
		r.Errorf(format, args...)
	}
}

// must stops the test when err is not nil.
func must(r reporter, err error) {
	r.Helper()

	if err != nil {
		r.Fatal(err)
	}
}

func run(r reporter, e sandbox.Enforcer, plan sandbox.Plan, line string) result {
	r.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	var out bytes.Buffer

	code, err := sandbox.Execute(ctx, e, plan, sandbox.Command{
		Argv: []string{"sh", "-c", line}, Dir: r.TempDir(), Env: []string{"PATH=" + commandPath, "HOME=" + r.TempDir()}, Stdout: &out, Stderr: &out,
	})
	must(r, err)

	return result{out: out.String(), code: code}
}

func base(r reporter) sandbox.Plan {
	r.Helper()

	return sandbox.Plan{Read: sandbox.SystemRead(), Write: append([]string{r.TempDir()}, sandbox.SystemWrite()...)}
}

// homeFolder makes a folder under the home folder of the person who runs the test, and removes it at the end.
func homeFolder(r reporter) string {
	r.Helper()

	home, err := os.UserHomeDir()
	must(r, err)

	dir, err := os.MkdirTemp(home, "obee-contract-")
	must(r, err)

	r.Cleanup(func() { must(r, os.RemoveAll(dir)) })

	return dir
}

func readUnderHome(r reporter, e sandbox.Enforcer) {
	r.Helper()

	secret := filepath.Join(homeFolder(r), "secret")
	must(r, os.WriteFile(secret, []byte("hunter2"), secretMode))

	plan := base(r)

	denied := run(r, e, plan, "cat "+secret)
	expect(r, denied.code != 0, "the home folder is not in the plan, so the read fails, and it exited %d: %s", denied.code, denied.out)
	expect(r, !strings.Contains(denied.out, "hunter2"), "the denied read printed the secret: %s", denied.out)

	plan.Read = append(plan.Read, filepath.Dir(secret))
	allowed := run(r, e, plan, "cat "+secret)
	expect(r, allowed.code == 0, "the read of a listed folder failed: %s", allowed.out)
	expect(r, strings.Contains(allowed.out, "hunter2"), "the same read works when the plan lists the folder: %s", allowed.out)
}

func writeOutside(r reporter, e sandbox.Enforcer) {
	r.Helper()

	plan := base(r)
	inside := filepath.Join(plan.Write[0], "ok")
	outside := filepath.Join(homeFolder(r), "bad")

	expect(r, run(r, e, plan, "echo x > "+inside).code == 0, "a write inside an allowed path works")
	expect(r, exists(inside), "the write inside an allowed path left no file")

	denied := run(r, e, plan, "echo x > "+outside)
	expect(r, denied.code != 0, "a write outside the plan exited 0: %s", denied.out)
	expect(r, !exists(outside), "a write outside the plan made a file")
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func exitCode(r reporter, e sandbox.Enforcer) {
	r.Helper()

	for line, want := range map[string]int{"exit 0": 0, "exit " + strconv.Itoa(exitSeven): exitSeven, "obee-no-such-program": exitNotFound} {
		got := run(r, e, base(r), line).code
		expect(r, got == want, "%q exited %d, want %d", line, got, want)
	}
}

// serve starts a local server on 127.0.0.1 that answers "pong" to any request, with TLS when withTLS is true. It returns the host and the URL.
// The server stops, and its goroutines end, at the end of the test.
func serve(r reporter, withTLS bool) (host, url string) {
	r.Helper()

	listener, scheme := listen(r, withTLS)

	answering(r, listener)

	address := listener.Addr().String()

	return hostOf(r, address), scheme + "://" + address
}

// answering answers every connection of the listener until the end of the test.
func answering(r reporter, listener net.Listener) {
	r.Helper()

	stopped := make(chan struct{})

	go func() {
		acceptAll(listener)
		close(stopped)
	}()

	r.Cleanup(func() {
		_ = listener.Close()
		<-stopped
	})
}

// listen opens the listener of the test server on 127.0.0.1, and returns it with the scheme of its URLs.
func listen(r reporter, withTLS bool) (net.Listener, string) {
	r.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	must(r, err)

	if !withTLS {
		return listener, "http"
	}

	return tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{selfSigned(r)}, MinVersion: tls.VersionTLS12}), "https"
}

func hostOf(r reporter, address string) string {
	r.Helper()

	host, _, err := net.SplitHostPort(address)
	must(r, err)

	return host
}

// acceptAll answers every connection until the listener closes.
func acceptAll(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		go answer(conn)
	}
}

// answer reads one request head and writes the answer "pong". A connection that ends before the head is complete gets no answer.
func answer(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		if strings.TrimSpace(line) == "" {
			break
		}
	}

	fmt.Fprint(conn, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\nConnection: close\r\n\r\npong")
}

// selfSigned makes a certificate for 127.0.0.1. The suite runs curl with --insecure, so no one has to trust it.
func selfSigned(r reporter) tls.Certificate {
	r.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(r, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "obee-contract"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(certLifetime), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	must(r, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// fetch is the command that gets a URL and prints the body and the status. curl reads HTTP_PROXY in lower case and HTTPS_PROXY in upper case, and the proxy
// variables that Execute sets are the only way it can reach the server.
func fetch(url string, extra ...string) string {
	return fmt.Sprintf("curl --fail --silent --show-error --max-time 20 %s -w ' status=%%{http_code}' %s", strings.Join(extra, " "), url)
}

// networkPlan is the base plan with network and the listed domains. The network checks need curl, and a machine without it fails them.
func networkPlan(r reporter, domains ...string) sandbox.Plan {
	r.Helper()

	_, err := exec.LookPath("curl")
	must(r, err)

	plan := base(r)
	plan.Network, plan.Domains = true, domains

	return plan
}

func domains(r reporter, e sandbox.Enforcer) {
	r.Helper()

	domainsOver(r, e, false)
	domainsOver(r, e, true)
}

// domainsOver checks a listed and an unlisted host over plain HTTP, or over an HTTPS tunnel when withTLS is true.
func domainsOver(r reporter, e sandbox.Enforcer, withTLS bool) {
	r.Helper()

	host, url := serve(r, withTLS)

	// The test server has the address 127.0.0.1. The name localhost reaches the same server and is not listed.
	listed := run(r, e, networkPlan(r, host), fetch(url, "--insecure"))
	expect(r, strings.Contains(listed.out, "pong") && listed.code == 0, "a listed domain failed with %d: %s", listed.code, listed.out)

	other := strings.Replace(url, "127.0.0.1", "localhost", 1)
	unlisted := run(r, e, networkPlan(r, host), fetch(other, "--insecure"))
	expect(r, !strings.Contains(unlisted.out, "pong") && unlisted.code != 0, "an unlisted domain got through with %d: %s", unlisted.code, unlisted.out)

	empty := run(r, e, networkPlan(r), fetch(url, "--insecure"))
	expect(r, !strings.Contains(empty.out, "pong"), "an empty list lets nothing through: %s", empty.out)
}

func direct(r reporter, e sandbox.Enforcer) {
	r.Helper()

	host, url := serve(r, false)

	skipped := run(r, e, networkPlan(r, host), fetch(url, "--noproxy '*'"))
	expect(r, !strings.Contains(skipped.out, "pong") && skipped.code != 0, "the command may connect to the proxy port only, and it exited %d: %s", skipped.code, skipped.out)
}

func noNetwork(r reporter, e sandbox.Enforcer) {
	r.Helper()

	_, url := serve(r, false)

	plan := networkPlan(r)
	plan.Network = false

	closed := run(r, e, plan, fetch(url, "--noproxy '*'"))
	expect(r, !strings.Contains(closed.out, "pong") && closed.code != 0, "a plan with no network connected, and it exited %d: %s", closed.code, closed.out)
}

// unixSocket starts servers on a pathname socket in a folder outside the plan, and on Linux on an abstract socket, and proves that the command reaches
// neither, even though the plan opens the network. A command that reaches such a socket runs as its owner outside the sandbox (a socket of a container
// service or of a key agent).
func unixSocket(r reporter, e sandbox.Enforcer) {
	r.Helper()

	addresses, sockets := socketNames(r)

	for kind, address := range addresses {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", address)
		must(r, err)
		answering(r, listener)

		reached := run(r, e, networkPlan(r), fetch("http://localhost/", sockets[kind]))
		expect(r, !strings.Contains(reached.out, "pong") && reached.code != 0, "the command reached the %s unix socket outside the plan, and it exited %d: %s", kind, reached.code, reached.out)
	}
}

// socketNames returns, by kind, the address of each unix socket to serve and the curl flag that connects to it. The pathname socket lies in a new folder
// that no plan lists. Only Linux has abstract sockets.
func socketNames(r reporter) (addresses, flags map[string]string) {
	r.Helper()

	folder, err := os.MkdirTemp("", "obee-sock-")
	must(r, err)
	r.Cleanup(func() { must(r, os.RemoveAll(folder)) })

	path := filepath.Join(folder, "s")
	addresses, flags = map[string]string{"pathname": path}, map[string]string{"pathname": "--unix-socket " + path}

	if runtime.GOOS == "linux" {
		name := fmt.Sprintf("obee-contract-%d-%d", os.Getpid(), time.Now().UnixNano())
		addresses["abstract"], flags["abstract"] = "@"+name, "--abstract-unix-socket "+name
	}

	return addresses, flags
}

// wholeFolder proves that a folder the plan lets the command write is writable whole: the command makes a new top-level file and a new top-level folder
// with a file in it, then moves the file and removes both.
func wholeFolder(r reporter, e sandbox.Enforcer) {
	r.Helper()

	plan := base(r)
	work := plan.Write[0]
	file, folder := filepath.Join(work, "new.txt"), filepath.Join(work, "newdir")

	made := run(r, e, plan, "cd "+work+" && echo x > new.txt && mkdir newdir && echo y > newdir/inner && mv new.txt newdir/moved")
	expect(r, made.code == 0 && exists(filepath.Join(folder, "moved")) && !exists(file), "a new top-level file or folder could not be made, and it exited %d: %s", made.code, made.out)

	removed := run(r, e, plan, "cd "+work+" && rm -r newdir")
	expect(r, removed.code == 0 && !exists(folder), "a new top-level folder could not be removed, and it exited %d: %s", removed.code, removed.out)
}
