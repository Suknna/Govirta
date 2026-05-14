package qemu

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/virt/qemuimg"
)

func TestIntegrationCirrOSWithTap(t *testing.T) {
	if os.Getenv("GOVIRTA_QEMU_INTEGRATION") != "1" {
		t.Skip("set GOVIRTA_QEMU_INTEGRATION=1 to run QEMU integration test")
	}

	image := requiredEnv(t, "GOVIRTA_CIRROS_IMAGE")
	binary := requiredEnv(t, "GOVIRTA_QEMU_BINARY")
	firmware := requiredEnv(t, "GOVIRTA_QEMU_FIRMWARE")
	tapName := requiredEnv(t, "GOVIRTA_QEMU_TAP")
	qemuimgBinary := os.Getenv("GOVIRTA_QEMUIMG_BINARY")
	if qemuimgBinary == "" {
		qemuimgBinary = "qemu-img"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	imageInfo, err := qemuimg.NewClient(qemuimgBinary, qemuimg.OSRunner{}).Info(ctx, image)
	if err != nil {
		t.Fatalf("qemuimg.Info(%q) error = %v", image, err)
	}
	if imageInfo.Format != qemuimg.FormatQCOW2 {
		t.Fatalf("qemuimg.Info(%q).Format = %q, want %q", image, imageInfo.Format, qemuimg.FormatQCOW2)
	}

	runDir := filepath.Join(".tmp", "qemu", "integration")
	if remoteRunDir := os.Getenv("GOVIRTA_QEMU_RUN_DIR"); remoteRunDir != "" {
		runDir = remoteRunDir
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", runDir, err)
	}

	cfg := Config{
		Binary:   binary,
		Name:     "cirros-dev-tap",
		Machine:  MachineConfig{Type: "virt", Accelerator: AcceleratorTCG},
		Compute:  ComputeConfig{MemoryMiB: 256, VCPUs: 1, CPUModel: "cortex-a57"},
		Firmware: FirmwareConfig{BIOSPath: firmware},
		QMP:      QMPConfig{SocketPath: filepath.Join(runDir, "qmp.sock")},
		Disks:    []DiskConfig{{ID: "root", Path: image, Format: DiskFormatQCOW2, Interface: DiskInterfaceVirtio}},
		NICs:     []NICConfig{{ID: "net0", Model: NICModelVirtioNetPCI, MAC: "52:54:00:12:34:56", Tap: TapBackendConfig{IfName: tapName}}},
		Logging:  LoggingConfig{QEMULogPath: filepath.Join(runDir, "qemu.log")},
		Process:  ProcessConfig{PIDFilePath: filepath.Join(runDir, "qemu.pid")},
	}

	serialPath := filepath.Join(runDir, "serial.log")
	cfg.Console.SerialLogPath = serialPath

	proc, err := NewDefaultDriver().Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = proc.Stop(stopCtx)
	}()

	waitForQMP(t, cfg.QMP.SocketPath, 30*time.Second)
	waitForSerialMarker(t, serialPath, "eth0: carrier acquired", 180*time.Second)
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is required", key)
	}
	return value
}

func waitForQMP(t *testing.T, socketPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		if err == nil {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			buf := make([]byte, 4096)
			if _, err := conn.Read(buf); err != nil {
				t.Fatalf("read QMP greeting error = %v", err)
			}
			if err := json.NewEncoder(conn).Encode(map[string]string{"execute": "qmp_capabilities"}); err != nil {
				t.Fatalf("write qmp_capabilities error = %v", err)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("QMP socket %q not ready within %s", socketPath, timeout)
}

func waitForSerialMarker(t *testing.T, path string, marker string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), marker) {
			return
		}
		time.Sleep(time.Second)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("serial marker %q not found within %s; serial tail: %s", marker, timeout, tailString(string(data), 2000))
}

func tailString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
