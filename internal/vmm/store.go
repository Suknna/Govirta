package vmm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/suknna/govirta/internal/vmm/proc"
)

// encodeState 把 persistedState 编码为缩进 JSON（人类可读，便于运维排障）。
func encodeState(s persistedState) ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("vmm: encode state: %w", err)
	}
	return data, nil
}

// decodeState 解码 vm.json 并校验关键不变量（uuid 非空、intent 合法）。
func decodeState(data []byte) (persistedState, error) {
	var s persistedState
	if err := json.Unmarshal(data, &s); err != nil {
		return persistedState{}, fmt.Errorf("vmm: decode state: %w", err)
	}
	if s.UUID == "" {
		return persistedState{}, fmt.Errorf("%w: persisted state missing uuid", ErrInvalidRequest)
	}
	if !s.Intended.Valid() {
		return persistedState{}, fmt.Errorf("%w: persisted state invalid intent %q", ErrInvalidRequest, s.Intended)
	}
	return s, nil
}

// writeState 编码并经 ProcessController 原子落盘 vm.json，更新 UpdatedAt。
func (s *VMMService) writeState(ctx context.Context, st persistedState) error {
	st.UpdatedAt = time.Now().UTC()
	data, err := encodeState(st)
	if err != nil {
		return err
	}
	if err := s.proc.WriteState(ctx, st.Paths.StateFile, data); err != nil {
		return fmt.Errorf("vmm: persist state for %s: %w", st.UUID, err)
	}
	return nil
}

// loadState 读 vm.json 并解码；文件不存在映射为 ErrNotFound。
func (s *VMMService) loadState(ctx context.Context, uuid string) (persistedState, error) {
	paths := runtimePathsFor(s.runtimeRoot, uuid)
	data, err := s.proc.ReadState(ctx, paths.StateFile)
	if err != nil {
		if errors.Is(err, proc.ErrStateNotFound) {
			return persistedState{}, fmt.Errorf("%w: %s", ErrNotFound, uuid)
		}
		return persistedState{}, fmt.Errorf("vmm: read state for %s: %w", uuid, err)
	}
	return decodeState(data)
}
