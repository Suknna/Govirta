package vmm

import "errors"

// vmm sentinel 错误类。全部 %w 传播，errors.Is/As 可分类（spec §10）。
var (
	// ErrInvalidRequest 表示调用方输入非法（空 uuid、nil builder、非法持久态等）。
	ErrInvalidRequest = errors.New("vmm: invalid request")
	// ErrNotFound 表示目标 VM 的 vm.json 不存在。
	ErrNotFound = errors.New("vmm: vm not found")
	// ErrAlreadyExists 表示同 uuid 的 vm.json 已存在（Create 不覆盖）。
	ErrAlreadyExists = errors.New("vmm: vm already exists")
	// ErrConflict 表示状态冲突（如 Delete 一个仍有 live 进程的 VM）。
	ErrConflict = errors.New("vmm: vm state conflict")
	// ErrNotReady 表示目标尚不可用于该操作（如 Reattach 一个进程已死的 VM）。
	ErrNotReady = errors.New("vmm: vm not ready")
)
