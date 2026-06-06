package proc

import "errors"

// ErrStateNotFound 表示 vm.json 不存在（ReadState）。vmm 据此判定 ErrNotFound。
var ErrStateNotFound = errors.New("proc: state file not found")
