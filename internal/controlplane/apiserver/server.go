package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/suknna/govirta/internal/controlplane/mac"
	"github.com/suknna/govirta/internal/controlplane/scheduler"
	"github.com/suknna/govirta/internal/controlplane/store"
)

// shutdownGrace bounds how long a ctx-cancelled Run waits for in-flight requests
// to drain before forcibly closing connections. It is an internal robustness
// bound (not a placement/behavior knob), so it is a named constant rather than a
// caller-injected field: a long-lived watch must not let a cancelled Run hang
// forever, but a brief apply/get must be allowed to finish cleanly.
const shutdownGrace = 10 * time.Second

// Server is the control-plane HTTP surface. It is the admission boundary between
// caller-submitted resource JSON and the raw store: it hosts the apply, get,
// list/watch, and status handlers and owns their collaborators.
//
// All collaborators are constructor-injected (显式优于隐式) so unit tests can
// substitute the in-memory fake store, a deterministic MAC pool, and a noop
// scheduler. addr is supplied by the caller — the package never defaults a
// listen address. Passing a nil store, allocator, or scheduler yields a Server
// that panics on first use, surfacing wiring mistakes immediately rather than
// silently dropping writes.
type Server struct {
	store store.Store
	alloc mac.MACAllocator
	// sched places a VM onto a node at apply time when the VM carries no explicit
	// node binding (see the VM branch of apply).
	sched scheduler.Scheduler
	// nodeNames is the static set of candidate nodes the scheduler chooses from.
	// It is caller-injected configuration (第一刀：静态 node 名单), not discovered.
	nodeNames []string
	// addr is the TCP listen address Run binds. The caller passes it explicitly;
	// "" would bind an arbitrary port, so callers that want a fixed port must say so.
	addr string
}

// NewServer constructs a Server over its required collaborators and listen
// address. st, alloc, and sched are mandatory; nodeNames is the static placement
// candidate set; addr is the TCP address Run will listen on. None are defaulted —
// every behavior-affecting input is the caller's explicit choice.
func NewServer(st store.Store, alloc mac.MACAllocator, sched scheduler.Scheduler, nodeNames []string, addr string) *Server {
	return &Server{
		store:     st,
		alloc:     alloc,
		sched:     sched,
		nodeNames: nodeNames,
		addr:      addr,
	}
}

// Handler returns the HTTP routes this server serves on a single /apis surface.
// Apply is bound to both POST and PUT on /apis/{kind}/{name}: the create/update
// distinction is not meaningful at this layer (the store performs an
// unconditional create-or-overwrite), so both verbs share one handler. The read
// (get/list/watch) and status sub-resource routes are registered alongside apply
// so one Handler() serves the full surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /apis/{kind}/{name}", s.Apply)
	mux.HandleFunc("PUT /apis/{kind}/{name}", s.Apply)
	// Read routes (get/list) are registered alongside apply so a single
	// Handler() serves the full /apis surface.
	s.getHandler(mux)
	// Status sub-resource (up-reconcile) shares the same Handler() so node
	// status reports land on the one /apis surface as apply and get.
	s.statusHandler(mux)
	// Delete (finalizer two-phase entry: stamp deletionTimestamp) shares the
	// same Handler() so a DELETE lands on the one /apis surface as the rest.
	s.deleteHandler(mux)
	return mux
}

// Run binds the configured TCP address and serves the /apis surface until ctx is
// cancelled, at which point it gracefully shuts the listener down. It is the
// 积木式 contract the controlplane assembly layer depends on (Run(ctx) error),
// unchanged from the skeleton it replaces. A failure to bind addr is returned
// immediately; a clean ctx-triggered shutdown returns nil.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("apiserver: listen on %q: %w", s.addr, err)
	}
	return s.serve(ctx, ln)
}

// serve runs the HTTP server over an already-bound listener. It is the testable
// core of Run: tests bind a "127.0.0.1:0" listener, read its real port, and
// exercise routing and ctx-cancel teardown without racing an OS-chosen port
// through Run.
//
// ctx is threaded into every request via BaseContext (ctx 端到端), so cancelling
// it both cancels in-flight request contexts (a streaming watch observes it and
// returns) and triggers a graceful Shutdown. The Shutdown watcher goroutine has
// an explicit exit on both arms — ctx.Done (shut down) and serveDone (Serve ended
// on its own, e.g. a listener error) — so it can never leak (无 fire-and-forget).
func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:     s.Handler(),
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	serveDone := make(chan struct{})
	var shutdownErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			// Parent cancelled: stop accepting and drain in-flight requests with a
			// bounded grace. A fresh context is used because ctx is already done.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			shutdownErr = srv.Shutdown(shutdownCtx)
		case <-serveDone:
			// Serve returned on its own (listener error); there is nothing to shut
			// down and no parent cancellation to wait for. Exit cleanly.
		}
	}()

	serveErr := srv.Serve(ln)
	close(serveDone)
	wg.Wait()

	// A graceful Shutdown makes Serve return ErrServerClosed; that is the expected
	// success path, so surface only the shutdown outcome (nil on a clean drain).
	if errors.Is(serveErr, http.ErrServerClosed) {
		return shutdownErr
	}
	// Otherwise Serve failed for a real reason (e.g. the listener broke); join any
	// shutdown error so no failure is swallowed.
	return errors.Join(serveErr, shutdownErr)
}
