package remove

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	imgexec "github.com/suknna/govirta/pkg/virt/qemuimg/internal/exec"
)

func TestDoRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat() error = %v, want not exist", err)
	}
}

func TestDoRequiresPath(t *testing.T) {
	err := New("qemu-img", nil).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
}

func TestDoRejectsNonQCOW2Path(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.raw")
	if err := os.WriteFile(path, []byte("raw"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v, want file to remain", err)
	}
}

func TestDoRejectsQCOW2BackupSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2.bak")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v, want file to remain", err)
	}
}

func TestDoRemovesPathWithCaseInsensitiveQCOW2Suffix(t *testing.T) {
	for _, name := range []string{"disk.QCOW2", "disk.QcOw2"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, []byte("qcow2"), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			err := New("qemu-img", nil).Path(path).Do(context.Background())

			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Stat() error = %v, want not exist", err)
			}
		})
	}
}

func TestDoRejectsDirectoryEvenWithQCOW2Suffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("Stat() = (%v, %v), want directory to remain", info, err)
	}
}

func TestDoRejectsSymlinkEvenWithQCOW2Suffix(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.qcow2")
	if err := os.WriteFile(target, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	link := filepath.Join(dir, "disk.qcow2")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	err := New("qemu-img", nil).Path(link).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("Lstat() error = %v, want symlink to remain", err)
	}
}

func TestDoRejectsNonRegularFileEvenWithQCOW2Suffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("Mkfifo() unsupported: %v", err)
	}

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat() error = %v, want FIFO to remain", err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("Lstat().Mode() = %v, want FIFO to remain", info.Mode())
	}
}

func TestDoRejectsLeadingDashPath(t *testing.T) {
	err := New("qemu-img", nil).Path("--help.qcow2").Do(context.Background())

	if !errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, want invalid request", err)
	}
}

func TestDoReturnsErrorForMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.qcow2")

	err := New("qemu-img", nil).Path(path).Do(context.Background())

	if err == nil {
		t.Fatal("Do() error = nil, want error")
	}
}

func TestDoSurfacesLstatErrorOtherThanNotExist(t *testing.T) {
	// 回归 F8：Lstat 失败但不是 NotExist（例如父目录无执行权限）时，
	// 必须显式透出错误，而不是被 os.Remove 的二次错误掩盖真实根因。
	// 通过把目标文件放到 chmod 000 的目录里来诱发 EACCES。
	dir := t.TempDir()
	restricted := filepath.Join(dir, "noaccess")
	if err := os.Mkdir(restricted, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	target := filepath.Join(restricted, "disk.qcow2")
	if err := os.WriteFile(target, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(restricted, 0o000); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(restricted, 0o700) })
	// root 用户能绕开权限位，跳过避免误判。
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses permission check")
	}

	err := New("qemu-img", nil).Path(target).Do(context.Background())

	if err == nil {
		t.Fatalf("Do() error = nil, want lstat error")
	}
	// 不应是 InvalidRequest（那是输入校验类）；不应是 NotExist
	// （那应该让 os.Remove 处理）。应该携带「stat <path>:」前缀以表明
	// 是 Lstat 阶段的错误，并保留底层 EACCES 链便于调试。
	if errors.Is(err, imgexec.ErrInvalidRequest) {
		t.Fatalf("Do() error = %v, should not be InvalidRequest", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Do() error = %v, should not be NotExist", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Do() error = %v, want errors.Is(err, fs.ErrPermission)", err)
	}
}

func TestDoDoesNotRemoveFileWhenContextCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("qcow2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := New("qemu-img", nil).Path(path).Do(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context canceled", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v, want file to remain", err)
	}
}
