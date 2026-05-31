//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suknna/govirta/internal/virt/qemuimg"
)

func TestQEMUImgLifecycle(t *testing.T) {
	qemuImgPath := requireQEMUImgAcceptanceEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const (
		sourceSize  int64 = 1 << 20
		createdSize int64 = 8 << 20
		resizedSize int64 = 2 << 20
	)

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.raw")
	createdPath := filepath.Join(dir, "created.qcow2")
	convertedPath := filepath.Join(dir, "converted.qcow2")

	if err := os.WriteFile(sourcePath, make([]byte, sourceSize), 0600); err != nil {
		t.Fatalf("create raw source image: %v", err)
	}

	qcow2 := qemuimg.NewClient(qemuimg.Config{Binary: qemuImgPath}).QCOW2()
	if err := qcow2.Create().Target(createdPath).SizeBytes(createdSize).Do(ctx); err != nil {
		t.Fatalf("create qcow2 image: %v", err)
	}

	createdInfo, err := qcow2.Info().Path(createdPath).Do(ctx)
	if err != nil {
		t.Fatalf("read created qcow2 info: %v", err)
	}
	if createdInfo.Format != "qcow2" {
		t.Fatalf("created image format = %q, want qcow2", createdInfo.Format)
	}
	if createdInfo.VirtualSize != createdSize {
		t.Fatalf("created image virtual size = %d, want %d", createdInfo.VirtualSize, createdSize)
	}

	if err := qcow2.Convert().Source(sourcePath).SourceFormat("raw").Target(convertedPath).Do(ctx); err != nil {
		t.Fatalf("convert raw image to qcow2: %v", err)
	}

	convertedInfo, err := qcow2.Info().Path(convertedPath).Do(ctx)
	if err != nil {
		t.Fatalf("read converted qcow2 info: %v", err)
	}
	if convertedInfo.Format != "qcow2" {
		t.Fatalf("converted image format = %q, want qcow2", convertedInfo.Format)
	}

	if err := qcow2.Resize().Path(convertedPath).SizeBytes(resizedSize).Do(ctx); err != nil {
		t.Fatalf("resize converted qcow2 image: %v", err)
	}

	resizedInfo, err := qcow2.Info().Path(convertedPath).Do(ctx)
	if err != nil {
		t.Fatalf("read resized qcow2 info: %v", err)
	}
	if resizedInfo.VirtualSize != resizedSize {
		t.Fatalf("resized image virtual size = %d, want %d", resizedInfo.VirtualSize, resizedSize)
	}

	if err := qcow2.Snapshot().Path(convertedPath).Name("acceptance-snap").Do(ctx); err != nil {
		t.Fatalf("create internal qcow2 snapshot: %v", err)
	}

	checkResult, err := qcow2.Check().Path(convertedPath).Do(ctx)
	if err != nil {
		t.Fatalf("check converted qcow2 image: %v", err)
	}
	if checkResult.CheckErrors != 0 {
		t.Fatalf("converted image check errors = %d, want 0", checkResult.CheckErrors)
	}
	if checkResult.Corruptions != 0 {
		t.Fatalf("converted image corruptions = %d, want 0", checkResult.Corruptions)
	}

	if err := qcow2.Remove().Path(convertedPath).Do(ctx); err != nil {
		t.Fatalf("remove converted qcow2 image: %v", err)
	}
	if _, err := os.Stat(convertedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed converted image stat error = %v, want not exist", err)
	}
}
