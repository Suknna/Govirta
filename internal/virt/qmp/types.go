package qmp

import "time"

// DefaultTimeout is the socket dial timeout used when Config.Timeout is empty.
const DefaultTimeout = 2 * time.Second

// Config configures a socket-backed QMP client.
type Config struct {
	SocketPath string
	Timeout    time.Duration
}

// State is the QEMU run-state returned by query-status.
type State string

const (
	// StateRunning means QEMU reports the VM is actively running.
	StateRunning State = "running"
	// StatePaused means QEMU reports the VM is paused.
	StatePaused State = "paused"
	// StateShutdown means QEMU reports the VM has shut down.
	StateShutdown State = "shutdown"
	// StatePrelaunch means QEMU has not started guest execution yet.
	StatePrelaunch State = "prelaunch"
	// StateInMigrate means QEMU is currently in migration state.
	StateInMigrate State = "inmigrate"
)

// Status is the typed result of QMP query-status.
type Status struct {
	Running    bool
	Singlestep bool
	State      State
}

// EventName is a QMP event name.
type EventName string

const (
	// EventShutdown is emitted when QEMU completes shutdown.
	EventShutdown EventName = "SHUTDOWN"
	// EventReset is emitted when QEMU resets the VM.
	EventReset EventName = "RESET"
	// EventStop is emitted when QEMU stops VM execution.
	EventStop EventName = "STOP"
)

// Event is the project-owned representation of a QMP event.
type Event struct {
	Name      EventName
	Data      map[string]any
	Timestamp time.Time
}

type commandName string

const (
	commandQueryStatus     commandName = "query-status"
	commandSystemPowerdown commandName = "system_powerdown"
	commandQuit            commandName = "quit"
)
