package flow

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	sampleEvery = 5 * time.Millisecond
	lsofTimeout = 10 * time.Second
)

// errNoSocketTool means the OS offers no way to list the sockets of this process.
var errNoSocketTool = errors.New("no way to list the sockets of this process")

// inetSockets lists the inet sockets this process holds, read from the OS: lsof on darwin, /proc on linux.
func inetSockets() ([]string, error) {
	switch runtime.GOOS {
	case "darwin":
		return lsofSockets()
	case "linux":
		return procSockets()
	default:
		return nil, fmt.Errorf("%w on %s", errNoSocketTool, runtime.GOOS)
	}
}

func lsofSockets() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lsofTimeout)
	defer cancel()

	// lsof lists the inet sockets of every process, and the loop below keeps the ones this process holds.
	out, err := exec.CommandContext(ctx, "lsof", "-i", "-n", "-P", "-Fpn").Output()

	var exit *exec.ExitError

	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, fmt.Errorf("%w: lsof is not installed", errNoSocketTool)
	case errors.As(err, &exit) && len(out) == 0:
		return nil, nil // lsof exits 1 when the process has no inet file
	case err != nil:
		return nil, err
	}

	var (
		sockets []string
		mine    bool
	)

	own := "p" + strconv.Itoa(os.Getpid())

	for line := range strings.Lines(string(out)) {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "p") {
			mine = line == own
		} else if name, found := strings.CutPrefix(line, "n"); found && mine {
			sockets = append(sockets, name)
		}
	}

	return sockets, nil
}

// procSockets joins the socket inodes this process holds open with the inet tables of its network namespace.
func procSockets() ([]string, error) {
	held := map[string]bool{}

	links, err := filepath.Glob("/proc/self/fd/*")
	if err != nil {
		return nil, err
	}

	for _, link := range links {
		if target, readErr := os.Readlink(link); readErr == nil && strings.HasPrefix(target, "socket:[") {
			held[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}

	root, err := os.OpenRoot("/proc/self/net")
	if err != nil {
		return nil, err
	}

	defer func() { _ = root.Close() }()

	var sockets []string

	for _, table := range []string{"tcp", "tcp6", "udp", "udp6"} {
		rows, tableErr := heldRows(root, table, held)
		if tableErr != nil {
			return nil, tableErr
		}

		sockets = append(sockets, rows...)
	}

	return sockets, nil
}

// heldRows reads one inet table and returns the local address of each row whose inode this process holds open.
func heldRows(root *os.Root, table string, held map[string]bool) ([]string, error) {
	file, err := root.Open(table)
	if err != nil {
		return nil, err
	}

	var rows []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > inodeColumn && held[fields[inodeColumn]] {
			rows = append(rows, table+" "+fields[localColumn])
		}
	}

	return rows, errors.Join(scanner.Err(), file.Close())
}

const (
	localColumn = 1
	inodeColumn = 9
)

// watchSockets runs work and samples the inet sockets of the process until it ends. It returns every socket it saw.
// The test is skipped where the OS has no tool to list them.
func watchSockets(t *testing.T, work func()) []string {
	t.Helper()

	if _, err := inetSockets(); errors.Is(err, errNoSocketTool) {
		t.Skipf("cannot check sockets here: %v", err)
	}

	var (
		mu      sync.Mutex
		seen    []string
		failure error
	)

	sample := func() {
		sockets, err := inetSockets()

		mu.Lock()
		defer mu.Unlock()

		if failure == nil {
			failure = err
		}

		for _, socket := range sockets {
			if !slices.Contains(seen, socket) {
				seen = append(seen, socket)
			}
		}
	}

	stop, finished := make(chan struct{}), make(chan struct{})

	go func() {
		defer close(finished)

		for {
			sample()

			select {
			case <-stop:
				return
			case <-time.After(sampleEvery):
			}
		}
	}()

	work()
	close(stop)
	<-finished
	sample()

	require.NoError(t, failure)

	return seen
}

func TestRun_OpensNoNetworkSocket(t *testing.T) { //nolint:paralleltest // the test samples the sockets of the whole process
	harness := newHarness(t)

	sockets := watchSockets(t, func() {
		engine := harness.engine(t)

		_, err := engine.Run(testItem, testProject)
		require.NoError(t, err)

		_, err = harness.client(t, testVersion).Status()
		require.NoError(t, err)
	})

	assert.Empty(t, sockets, "obee opens no network connection")
}

func TestInetSockets_SeesALoopbackDialAndItsListener(t *testing.T) { //nolint:paralleltest // the test opens sockets that TestRun_OpensNoNetworkSocket would see
	var held []string

	watchSockets(t, func() {
		listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		defer func() { require.NoError(t, listener.Close()) }()

		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", listener.Addr().String())
		require.NoError(t, err)

		defer func() { require.NoError(t, conn.Close()) }()

		var err2 error

		held, err2 = inetSockets()
		require.NoError(t, err2)
	})

	assert.NotEmpty(t, held, "a dial to 127.0.0.1 shows as an inet socket")
}
