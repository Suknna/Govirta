package qemuimg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	FormatQCOW2 = "qcow2"
	FormatRaw   = "raw"
)

var ErrInvalidRequest = errors.New("invalid qemu-img request")

type CommandRunner interface {
	Run(ctx context.Context, binary string, args []string) (CommandResult, error)
}

type CommandResult struct {
	Stdout string
	Stderr string
}

type OSRunner struct{}

func (r OSRunner) Run(ctx context.Context, binary string, args []string) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return CommandResult{Stdout: string(stdout), Stderr: string(exitErr.Stderr)}, err
		}
		return CommandResult{Stdout: string(stdout)}, err
	}
	return CommandResult{Stdout: string(stdout)}, nil
}

type Client interface {
	Create(ctx context.Context, req CreateRequest) error
	Info(ctx context.Context, path string) (ImageInfo, error)
	Resize(ctx context.Context, req ResizeRequest) error
}

type ExecClient struct {
	binary string
	runner CommandRunner
}

func NewClient(binary string, runner CommandRunner) *ExecClient {
	return &ExecClient{binary: binary, runner: runner}
}

func NewDefaultClient() *ExecClient {
	return NewClient("qemu-img", OSRunner{})
}

type CreateRequest struct {
	Path          string
	Format        string
	SizeBytes     int64
	BackingFile   string
	BackingFormat string
}

type ResizeRequest struct {
	Path      string
	SizeBytes int64
}

type ImageInfo struct {
	Filename        string
	Format          string
	VirtualSize     int64
	ActualSize      int64
	BackingFilename string
}

func (c *ExecClient) Create(ctx context.Context, req CreateRequest) error {
	if err := validateCreate(req); err != nil {
		return err
	}
	args := []string{"create", "-f", req.Format}
	if req.BackingFile != "" {
		args = append(args, "-b", req.BackingFile)
		if req.BackingFormat != "" {
			args = append(args, "-F", req.BackingFormat)
		}
	}
	args = append(args, req.Path, strconv.FormatInt(req.SizeBytes, 10))
	_, err := c.runner.Run(ctx, c.binary, args)
	return err
}

func (c *ExecClient) Info(ctx context.Context, path string) (ImageInfo, error) {
	if strings.TrimSpace(path) == "" {
		return ImageInfo{}, invalidRequest("path is required")
	}
	result, err := c.runner.Run(ctx, c.binary, []string{"info", "--output=json", path})
	if err != nil {
		return ImageInfo{}, err
	}
	return parseInfo(result.Stdout)
}

func (c *ExecClient) Resize(ctx context.Context, req ResizeRequest) error {
	if strings.TrimSpace(req.Path) == "" {
		return invalidRequest("path is required")
	}
	if req.SizeBytes <= 0 {
		return invalidRequest("size_bytes must be positive")
	}
	_, err := c.runner.Run(ctx, c.binary, []string{"resize", req.Path, strconv.FormatInt(req.SizeBytes, 10)})
	return err
}

func validateCreate(req CreateRequest) error {
	if strings.TrimSpace(req.Path) == "" {
		return invalidRequest("path is required")
	}
	if req.Format != FormatQCOW2 && req.Format != FormatRaw {
		return invalidRequest("unsupported format %q", req.Format)
	}
	if req.SizeBytes <= 0 {
		return invalidRequest("size_bytes must be positive")
	}
	if req.BackingFile != "" && req.BackingFormat == "" {
		return invalidRequest("backing_format is required when backing_file is set")
	}
	return nil
}

func parseInfo(raw string) (ImageInfo, error) {
	var payload struct {
		Filename        string `json:"filename"`
		Format          string `json:"format"`
		VirtualSize     int64  `json:"virtual-size"`
		ActualSize      int64  `json:"actual-size"`
		BackingFilename string `json:"backing-filename"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ImageInfo{}, err
	}
	return ImageInfo{
		Filename:        payload.Filename,
		Format:          payload.Format,
		VirtualSize:     payload.VirtualSize,
		ActualSize:      payload.ActualSize,
		BackingFilename: payload.BackingFilename,
	}, nil
}

func invalidRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}
