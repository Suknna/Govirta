// Package controlplane is the composition root of the control-plane process
// (govirtad): it wires the etcd store, the MAC allocator over a configured pool,
// the scheduler, and the HTTP apiserver into one runnable Service.
//
// Every behavior-affecting input arrives through Config from the caller
// (cmd/govirtad). The package injects no defaults of its own (显式优于隐式): what
// the caller passes is exactly what runs.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/rs/zerolog"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/suknna/govirta/internal/controlplane/apiserver"
	controlcontroller "github.com/suknna/govirta/internal/controlplane/controller"
	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
	"github.com/suknna/govirta/internal/controlplane/store/etcd"
)

// Config carries every input the control plane needs to assemble itself. There
// are no in-package defaults: the caller must supply all fields explicitly so
// the running configuration is exactly what was passed (显式优于隐式). Validation
// of the MAC pool fields and the listen address happens in the layers that
// consume them (mac.NewPool, apiserver), so a bad value surfaces as a wrapped
// construction error rather than being silently corrected.
type Config struct {
	// EtcdEndpoints is the list of etcd endpoints the store dials.
	EtcdEndpoints []string
	// EtcdDialTimeout bounds the initial etcd connection attempt.
	EtcdDialTimeout time.Duration
	// ListenAddr is the TCP address the apiserver binds (e.g. "0.0.0.0:8080").
	ListenAddr string
	// MACPrefix is the 3-byte locally-administered unicast OUI the MAC pool walks.
	MACPrefix net.HardwareAddr
	// MACSuffixStart and MACSuffixEnd bound the inclusive 24-bit suffix interval
	// of the MAC pool.
	MACSuffixStart uint32
	MACSuffixEnd   uint32
	// NodeNames is the static set of candidate node names the scheduler places
	// VMs onto (第一刀：静态 node 名单).
	NodeNames []string
	// TaskManager carries every explicit input for the phase-one control-plane
	// controller-manager. The composition root must provide all fields; there are
	// no hidden defaults inside controlplane.
	TaskManager controlcontroller.Config
}

// Service coordinates control-plane components. It owns the store it was built
// over (it constructed it, so it closes it) and the apiserver that serves the
// /apis surface.
type Service struct {
	apiServer   *apiserver.Server
	taskManager *controlcontroller.Manager
	// store is owned by the Service: NewService constructs it, and Run closes it
	// on return so the dialed etcd client does not leak past serving life.
	store store.Store
}

// NewService assembles the production control plane: an etcd-backed store, a MAC
// allocator over the configured pool, a noop scheduler, and the HTTP apiserver.
//
// It takes ctx because constructing the etcd store dials the cluster; ctx scopes
// that dial so a cancelled caller aborts construction (ctx 端到端). This deviates
// from the plan's `NewService(cfg)` sketch on purpose: etcd.New requires a ctx,
// and the real signature wins over the sketch. Any failure (etcd dial, malformed
// MAC pool) is wrapped with %w and returned; on assembly failure after the store
// is built, the store is closed and its close error joined so nothing leaks and
// no error is swallowed.
func NewService(ctx context.Context, cfg Config) (*Service, error) {
	st, err := etcd.New(ctx, clientv3.Config{
		Endpoints:   cfg.EtcdEndpoints,
		DialTimeout: cfg.EtcdDialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("controlplane: build etcd store: %w", err)
	}

	svc, err := newServiceWithStore(st, cfg)
	if err != nil {
		// The store was constructed but later assembly failed; close it so the
		// dialed etcd client does not leak, joining any close error with the cause.
		return nil, errors.Join(err, st.Close())
	}
	return svc, nil
}

// newServiceWithStore assembles a Service over an already-constructed store. It
// is the seam unit tests use to inject the in-memory fake store and avoid
// touching real etcd: it builds the MAC pool/allocator, the noop scheduler, and
// the apiserver without dialing anything. A malformed MAC pool config is the
// only assembly error.
func newServiceWithStore(st store.Store, cfg Config) (*Service, error) {
	pool, err := mac.NewPool(cfg.MACPrefix, cfg.MACSuffixStart, cfg.MACSuffixEnd)
	if err != nil {
		return nil, fmt.Errorf("controlplane: build MAC pool: %w", err)
	}
	alloc := mac.NewAllocator(pool, st)
	sched := scheduler.NewNoopScheduler()
	srv := apiserver.NewServer(st, alloc, sched, cfg.NodeNames, cfg.ListenAddr)
	mgr := controlcontroller.NewManager(st, cfg.TaskManager)
	return &Service{apiServer: srv, taskManager: mgr, store: st}, nil
}

// Run starts the apiserver and serves until ctx is cancelled, then closes the
// owned store. The Run(ctx) error contract is unchanged (积木式) and ctx is
// threaded straight into apiServer.Run (ctx 端到端). The serve error and the
// store-close error are joined so neither is swallowed.
func (s *Service) Run(ctx context.Context) error {
	zerolog.Ctx(ctx).Info().Str("component", "controlplane").Msg("starting control plane")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	managerErr := make(chan error, 1)
	go func() {
		managerErr <- s.taskManager.Run(runCtx)
	}()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- s.apiServer.Run(runCtx)
	}()

	var serverErrValue error
	var mgrErr error
	managerDone := false
	serverDone := false
	for !serverDone {
		select {
		case err := <-serverErr:
			serverErrValue = err
			serverDone = true
		case err := <-managerErr:
			managerDone = true
			if err != nil {
				mgrErr = err
				cancel()
				serverErrValue = <-serverErr
				serverDone = true
			}
		case <-ctx.Done():
			cancel()
			serverErrValue = <-serverErr
			serverDone = true
		}
		if managerDone && !serverDone {
			// A normal phase-one manager completion only means its initial proof task
			// reached terminal state; the apiserver must continue serving until ctx is
			// canceled or the server itself fails.
			managerErr = nil
		}
	}
	if !managerDone {
		cancel()
		mgrErr = <-managerErr
	}
	closeErr := s.store.Close()
	return errors.Join(serverErrValue, mgrErr, closeErr)
}
