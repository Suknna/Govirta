//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/virt/qmp"
)

const (
	acceptanceEnabledEnv = "GOVIRTA_ACCEPTANCE"
	envQEMU              = "GOVIRTA_ACCEPTANCE_QEMU"
	envQEMUImg           = "GOVIRTA_ACCEPTANCE_QEMU_IMG"
	envFirmware          = "GOVIRTA_ACCEPTANCE_FIRMWARE"
	envCirros            = "GOVIRTA_ACCEPTANCE_CIRROS"
)

type acceptanceEnv struct {
	QEMU     string
	QEMUImg  string
	Firmware string
	Cirros   string
}

type serialMarkerGroup struct {
	Name    string
	Markers []string
}

type serialMarkerResult struct {
	Output string
	Err    error
}

func requireAcceptanceEnv(t *testing.T) acceptanceEnv {
	t.Helper()

	if os.Getenv(acceptanceEnabledEnv) != "1" {
		t.Skipf("set %s=1 to run acceptance tests", acceptanceEnabledEnv)
	}

	return acceptanceEnv{
		QEMU:     requireExecutableEnv(t, envQEMU),
		QEMUImg:  requireExecutableEnv(t, envQEMUImg),
		Firmware: requireFileEnv(t, envFirmware),
		Cirros:   requireFileEnv(t, envCirros),
	}
}

func requireQEMUImgAcceptanceEnv(t *testing.T) string {
	t.Helper()

	if os.Getenv(acceptanceEnabledEnv) != "1" {
		t.Skipf("set %s=1 to run acceptance tests", acceptanceEnabledEnv)
	}

	return requireExecutableEnv(t, envQEMUImg)
}

func requireFileEnv(t *testing.T, name string) string {
	t.Helper()

	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required when %s=1", name, acceptanceEnabledEnv)
	}
	info, err := os.Stat(value)
	if err != nil {
		t.Fatalf("%s must point to an existing file: %v", name, err)
	}
	if info.IsDir() {
		t.Fatalf("%s must point to a file, got directory %q", name, value)
	}
	return value
}

func requireExecutableEnv(t *testing.T, name string) string {
	t.Helper()

	value := requireFileEnv(t, name)
	info, err := os.Stat(value)
	if err != nil {
		t.Fatalf("%s must point to an existing executable: %v", name, err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Fatalf("%s must point to an executable file, got mode %s for %q", name, info.Mode().Perm(), value)
	}
	return value
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), commandError(name, args, stdout.Bytes(), stderr.Bytes(), err)
}

func waitForQMPStatus(t *testing.T, ctx context.Context, socketPath string, want qmp.State) qmp.Status {
	t.Helper()

	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	var lastStatus qmp.Status
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("waiting for QMP status %q: %v", want, err)
		}

		status, err := queryQMPStatusOnce(ctx, socketPath)
		if err == nil {
			lastStatus = status
			if status.State == want {
				return status
			}
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			t.Fatalf("waiting for QMP status %q: %v", want, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}

	if lastErr != nil {
		t.Fatalf("timed out waiting for QMP status %q after last error: %v", want, lastErr)
	}
	t.Fatalf("timed out waiting for QMP status %q, last status was %q", want, lastStatus.State)
	return qmp.Status{}
}

func queryQMPStatusOnce(ctx context.Context, socketPath string) (qmp.Status, error) {
	client, err := qmp.NewSocketClient(qmp.Config{SocketPath: socketPath, Timeout: 2 * time.Second})
	if err != nil {
		return qmp.Status{}, err
	}
	if err := client.Connect(ctx); err != nil {
		return qmp.Status{}, err
	}

	status, err := client.QueryStatus(ctx)
	disconnectErr := client.Disconnect(context.Background())
	return status, errors.Join(err, disconnectErr)
}

func waitForSerialMarkerGroups(ctx context.Context, socketPath string, groups []serialMarkerGroup) (string, error) {
	conn, err := dialUnixSocket(ctx, socketPath)
	if err != nil {
		return "", fmt.Errorf("connect serial socket %q: %w", socketPath, err)
	}
	defer conn.Close()

	found := make([]string, len(groups))
	var captured strings.Builder
	buf := make([]byte, 4096)
	for {
		if err := ctx.Err(); err != nil {
			return captured.String(), fmt.Errorf("timed out waiting for serial markers %s: %w", describeMissingMarkerGroups(groups, found), err)
		}

		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			return captured.String(), fmt.Errorf("set serial read deadline: %w", err)
		}
		n, err := conn.Read(buf)
		if n > 0 {
			captured.Write(buf[:n])
			output := captured.String()
			for i, group := range groups {
				if found[i] != "" {
					continue
				}
				for _, marker := range group.Markers {
					if strings.Contains(output, marker) {
						found[i] = marker
						break
					}
				}
			}
			if allMarkerGroupsFound(found) {
				return output, nil
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return captured.String(), fmt.Errorf("serial socket closed before markers %s", describeMissingMarkerGroups(groups, found))
		}
		return captured.String(), fmt.Errorf("read serial socket: %w", err)
	}
}

func dialUnixSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	var lastErr error
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	for {
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), lastErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func allMarkerGroupsFound(found []string) bool {
	for _, marker := range found {
		if marker == "" {
			return false
		}
	}
	return true
}

func describeMissingMarkerGroups(groups []serialMarkerGroup, found []string) string {
	var missing []string
	for i, group := range groups {
		if found[i] == "" {
			missing = append(missing, group.Name)
		}
	}
	return strings.Join(missing, ", ")
}

func tailString(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[len(value)-maxBytes:]
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func stopQEMU(ctx context.Context, socketPath string, cmd *exec.Cmd) error {
	var cleanupErrs []error
	qmpCtx, qmpCancel := context.WithTimeout(ctx, 10*time.Second)
	defer qmpCancel()

	client, err := qmp.NewSocketClient(qmp.Config{SocketPath: socketPath, Timeout: 2 * time.Second})
	if err != nil {
		cleanupErrs = append(cleanupErrs, err)
	} else if err := client.Connect(qmpCtx); err != nil {
		cleanupErrs = append(cleanupErrs, err)
	} else {
		cleanupErrs = append(cleanupErrs, client.Quit(qmpCtx))
		cleanupErrs = append(cleanupErrs, client.Disconnect(qmpCtx))
	}

	if cmd == nil || cmd.Process == nil {
		return errors.Join(cleanupErrs...)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()
	if err := waitForProcess(waitCtx, waitCh); err == nil {
		return errors.Join(cleanupErrs...)
	} else {
		cleanupErrs = append(cleanupErrs, err)
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cleanupErrs = append(cleanupErrs, err)
	}
	killedWaitCtx, killedWaitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer killedWaitCancel()
	cleanupErrs = append(cleanupErrs, waitForProcess(killedWaitCtx, waitCh))
	return errors.Join(cleanupErrs...)
}

func waitForProcess(ctx context.Context, waitCh <-chan error) error {
	select {
	case err := <-waitCh:
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func shortSocketPath(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if len(path) >= 100 {
		t.Fatalf("unix socket path must be shorter than 100 characters, got %d: %s", len(path), path)
	}
	return path
}

func commandError(name string, args []string, stdout, stderr []byte, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("run %s %s: %w\nstdout:\n%s\nstderr:\n%s", name, strings.Join(args, " "), err, stdout, stderr)
}
