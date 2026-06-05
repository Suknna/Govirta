package vmm

import "path/filepath"

// 运行时目录内的固定文件名（vmm 私有布局，spec §5）。
const (
	stateFileName  = "vm.json"
	pidFileName    = "qemu.pid"
	qemuLogName    = "qemu.log"
	qmpSocketName  = "qmp.sock"
	vncSocketName  = "vnc.sock"
	consoleLogName = "console.log"
)

// runtimePathsFor 从 runtimeRoot + uuid 计算一台 VM 的全部运行时路径。
// 布局归 vmm 私有，绝不泄漏给上层（spec Q7/Q8，与 storage local.Driver 同构）。
func runtimePathsFor(runtimeRoot, uuid string) RuntimePaths {
	dir := filepath.Join(runtimeRoot, uuid)
	return RuntimePaths{
		Dir:        dir,
		StateFile:  filepath.Join(dir, stateFileName),
		PidFile:    filepath.Join(dir, pidFileName),
		QEMULog:    filepath.Join(dir, qemuLogName),
		QMPSocket:  filepath.Join(dir, qmpSocketName),
		VNCSocket:  filepath.Join(dir, vncSocketName),
		ConsoleLog: filepath.Join(dir, consoleLogName),
	}
}
