package vmm

import (
	"context"
	"errors"
	"sync"

	"github.com/suknna/govirta/internal/vmm/proc"
	"github.com/suknna/govirta/pkg/virt/qmp"
)

// fakeController 是 proc.ProcessController 的内存 fake，供 vmm 单测注入。
// 它用内存 map 模拟 vm.json 文件系统、可编程进程存活、并记录 spawn/kill 调用，
// 使 vmm 编排与状态机无需真实 QEMU 即可断言（spec §6/§11）。
type fakeController struct {
	mu sync.Mutex

	// states 以 StateFile 路径为键模拟磁盘上的 vm.json 字节。
	states map[string][]byte
	// aliveByPidfile 以 pidfile 路径为键编程进程存活探测结果。
	aliveByPidfile map[string]bool
	// stateDirs 是 ListStateDirs 返回的 uuid 目录名列表。
	stateDirs []string

	// 调用记录，供断言使用。
	spawnedArgv [][]string
	spawnCalls  int
	killCalls   int
	removeCalls int

	// spawnHook 在 SpawnDaemonized 成功返回前调用，供测试模拟「daemonize 后
	// 进程变为存活」（spawn 成功后标记 pidfile 存活）。
	spawnHook func()

	// 可编程错误注入。
	spawnErr            error
	aliveErr            error
	forceKillErr        error
	writeErr            error
	listErr             error
	listStateDirsCalled bool
}

func newFakeController() *fakeController {
	return &fakeController{
		states:         map[string][]byte{},
		aliveByPidfile: map[string]bool{},
	}
}

func (f *fakeController) SpawnDaemonized(ctx context.Context, argv []string, runtimeDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawnCalls++
	cp := append([]string(nil), argv...)
	f.spawnedArgv = append(f.spawnedArgv, cp)
	if f.spawnErr != nil {
		return f.spawnErr
	}
	if f.spawnHook != nil {
		f.spawnHook()
	}
	return nil
}

func (f *fakeController) ProcessAlive(ctx context.Context, pidfilePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.aliveErr != nil {
		return false, f.aliveErr
	}
	return f.aliveByPidfile[pidfilePath], nil
}

func (f *fakeController) ForceKill(ctx context.Context, pidfilePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killCalls++
	if f.forceKillErr != nil {
		return f.forceKillErr
	}
	// 模拟杀死：标记进程不再存活。
	f.aliveByPidfile[pidfilePath] = false
	return nil
}

func (f *fakeController) WriteState(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.states[path] = append([]byte(nil), data...)
	return nil
}

func (f *fakeController) ReadState(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.states[path]
	if !ok {
		return nil, proc.ErrStateNotFound
	}
	return append([]byte(nil), data...), nil
}

func (f *fakeController) RemoveState(ctx context.Context, runtimeDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	// 删除该目录下的所有 state 记录。
	for path := range f.states {
		if hasDirPrefix(path, runtimeDir) {
			delete(f.states, path)
		}
	}
	return nil
}

func (f *fakeController) ListStateDirs(ctx context.Context, runtimeRoot string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listStateDirsCalled = true
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]string(nil), f.stateDirs...), nil
}

// hasDirPrefix 报告 path 是否位于 dir 目录下。
func hasDirPrefix(path, dir string) bool {
	if len(path) <= len(dir) {
		return false
	}
	return path[:len(dir)] == dir && path[len(dir)] == '/'
}

// fakeQMPClient 是 qmp.Client 的 fake，可编程 query-status 与连接失败，
// 并记录 powerdown/quit 调用。
type fakeQMPClient struct {
	mu sync.Mutex

	connectErr   error
	queryStatus  qmp.Status
	queryErr     error
	waitReadyErr error
	powerdownErr error
	quitErr      error

	connectCalls   int
	powerdownCalls int
	quitCalls      int
	connected      bool
}

func (c *fakeQMPClient) Name() string { return "fake-qmp" }

func (c *fakeQMPClient) Connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectCalls++
	if c.connectErr != nil {
		return c.connectErr
	}
	c.connected = true
	return nil
}

func (c *fakeQMPClient) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	return nil
}

func (c *fakeQMPClient) QueryStatus(ctx context.Context) (qmp.Status, error) {
	if err := ctx.Err(); err != nil {
		return qmp.Status{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queryErr != nil {
		return qmp.Status{}, c.queryErr
	}
	return c.queryStatus, nil
}

func (c *fakeQMPClient) WaitReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitReadyErr
}

func (c *fakeQMPClient) SystemPowerdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.powerdownCalls++
	return c.powerdownErr
}

func (c *fakeQMPClient) Quit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quitCalls++
	return c.quitErr
}

func (c *fakeQMPClient) Events(ctx context.Context, names ...qmp.EventName) (<-chan qmp.Event, error) {
	return nil, errors.New("fake-qmp: events not supported")
}
