package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
	"github.com/suknna/govirta/internal/controlplane/store"
	metav1 "github.com/suknna/govirta/pkg/apis/meta/v1alpha1"
	taskv1 "github.com/suknna/govirta/pkg/apis/task/v1alpha1"
)

// Config carries every phase-one Task manager input explicitly.
type Config struct {
	NodeTaskName    string
	NodeTaskNode    string
	ClusterTaskName string
	OwnerName       string
	OwnerUID        string
	ExecutorID      string
	NoopMarker      string
}

// Manager owns the phase-one control-plane Task lifecycle loop.
type Manager struct {
	client       *TaskClient
	config       Config
	clusterExec  TaskExecutor
	ready        chan struct{}
	readyOnce    sync.Once
	terminalOnce sync.Once
	terminal     chan struct{}
}

// NewManager constructs a Manager over the raw store boundary.
func NewManager(st store.Store, cfg Config) *Manager {
	return &Manager{
		client:      NewTaskClient(st),
		config:      cfg,
		clusterExec: NoopClusterExecutor{ExecutorID: cfg.ExecutorID, NoopMarker: cfg.NoopMarker},
		ready:       make(chan struct{}),
		terminal:    make(chan struct{}),
	}
}

// Run seeds the phase-one NodeTask/ClusterTask and watches the NodeTask until it
// reaches a terminal phase. It exits on ctx cancellation.
func (m *Manager) Run(ctx context.Context) error {
	if err := m.validateConfig(); err != nil {
		return err
	}
	log := zerolog.Ctx(ctx).With().Str("component", "controlplane-task-manager").Logger()
	nodeTaskSpec, err := m.newNodeTask()
	if err != nil {
		return err
	}
	nodeTask, err := m.client.CreateOrGetTask(ctx, nodeTaskSpec)
	if err != nil {
		return err
	}
	clusterTaskSpec, err := m.newClusterTask()
	if err != nil {
		return err
	}
	clusterTask, err := m.client.CreateOrGetTask(ctx, clusterTaskSpec)
	if err != nil {
		return err
	}
	if !clusterTask.Status.Phase.Terminal() {
		runningStatus := taskv1.TaskStatus{Phase: taskv1.TaskPhaseRunning, ErrorClass: taskv1.TaskErrorClassNone}
		if _, err := m.client.PatchStatus(ctx, clusterTask.Name, runningStatus); err != nil {
			return err
		}
		status, err := m.clusterExec.Execute(ctx, clusterTask)
		if err != nil {
			return err
		}
		if _, err := m.client.PatchStatus(ctx, clusterTask.Name, status); err != nil {
			return err
		}
		log.Info().Str("operation", string(taskv1.TaskOperationNoopCluster)).Str("outcome", "success").Msg("control-plane cluster task completed")
	}
	m.readyOnce.Do(func() { close(m.ready) })
	return m.watchNodeTask(ctx, nodeTask.Name)
}

// Ready is closed after initial phase-one tasks have been created and the
// ClusterTask has been executed.
func (m *Manager) Ready() <-chan struct{} { return m.ready }

// NodeTaskTerminal is closed after the watched NodeTask reaches a terminal phase.
func (m *Manager) NodeTaskTerminal() <-chan struct{} { return m.terminal }

func (m *Manager) validateConfig() error {
	if m.config.NodeTaskName == "" || m.config.NodeTaskNode == "" || m.config.ClusterTaskName == "" || m.config.OwnerName == "" || m.config.OwnerUID == "" || m.config.ExecutorID == "" || m.config.NoopMarker == "" {
		return fmt.Errorf("controlplane controller: NodeTaskName, NodeTaskNode, ClusterTaskName, OwnerName, OwnerUID, ExecutorID, and NoopMarker are required")
	}
	return nil
}

func (m *Manager) watchNodeTask(ctx context.Context, name string) error {
	raw, err := m.client.store.Get(ctx, taskKey(name))
	if err != nil {
		return fmt.Errorf("controlplane controller: get node task %q before watch: %w", name, err)
	}
	task, err := decodeStoredTask(raw)
	if err != nil {
		return err
	}
	if task.Status.Phase.Terminal() {
		m.terminalOnce.Do(func() { close(m.terminal) })
		return nil
	}

	events, err := m.client.store.Watch(ctx, taskKey(name), raw.ResourceVersion)
	if err != nil {
		return fmt.Errorf("controlplane controller: watch node task %q: %w", name, err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, open := <-events:
			if !open {
				return nil
			}
			if len(ev.Object.Value) == 0 {
				continue
			}
			task, err := decodeStoredTask(ev.Object)
			if err != nil {
				return err
			}
			if task.Name != name || !task.Status.Phase.Terminal() {
				continue
			}
			m.terminalOnce.Do(func() { close(m.terminal) })
			return nil
		}
	}
}

func (m *Manager) newNodeTask() (taskv1.Task, error) {
	input, err := m.noopInput()
	if err != nil {
		return taskv1.Task{}, err
	}
	return taskv1.Task{
		TypeMeta: metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: metav1.ObjectMeta{
			Name:     m.config.NodeTaskName,
			UID:      m.config.NodeTaskName,
			NodeName: m.config.NodeTaskNode,
		},
		Spec:   taskv1.TaskSpec{Scope: taskv1.TaskScopeNode, OwnerKind: metav1.KindTask, OwnerName: m.config.OwnerName, OwnerUID: m.config.OwnerUID, Operation: taskv1.TaskOperationNoopNode, Input: input},
		Status: taskv1.TaskStatus{Phase: taskv1.TaskPhasePending},
	}, nil
}

func (m *Manager) newClusterTask() (taskv1.Task, error) {
	input, err := m.noopInput()
	if err != nil {
		return taskv1.Task{}, err
	}
	return taskv1.Task{
		TypeMeta:   metav1.TypeMeta{APIVersion: metav1.APIGroupVersion, Kind: metav1.KindTask},
		ObjectMeta: metav1.ObjectMeta{Name: m.config.ClusterTaskName, UID: m.config.ClusterTaskName},
		Spec:       taskv1.TaskSpec{Scope: taskv1.TaskScopeCluster, OwnerKind: metav1.KindTask, OwnerName: m.config.OwnerName, OwnerUID: m.config.OwnerUID, Operation: taskv1.TaskOperationNoopCluster, Input: input},
		Status:     taskv1.TaskStatus{Phase: taskv1.TaskPhasePending},
	}, nil
}

func (m *Manager) noopInput() (json.RawMessage, error) {
	data, err := json.Marshal(taskv1.NoopInput{Marker: m.config.NoopMarker})
	if err != nil {
		return nil, fmt.Errorf("controlplane controller: marshal noop input: %w", err)
	}
	return data, nil
}
