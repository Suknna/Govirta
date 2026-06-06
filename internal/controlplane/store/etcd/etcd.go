// Package etcd provides an etcd-backed store.Store implementation. It is the
// production persistence for the control plane and is interchangeable with the
// in-memory fake: both pass the same store.RunStoreContract behavioral suite.
//
// Mapping to etcd primitives: Put/Get/List/Delete go through the KV API,
// compare-and-swap is an etcd Txn comparing ModRevision, and Watch is
// clientv3.Watch. A resource's ResourceVersion is its ModRevision rendered as a
// base-10 string (strconv between int64 and string).
package etcd

import (
	"context"
	"fmt"
	"strconv"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/suknna/govirta/internal/controlplane/store"
)

// Store is an etcd-backed store.Store. Construct it with New; the zero value is
// not usable because it has no client.
type Store struct {
	// cli is the etcd client. All KV/Watch calls are issued against it and it is
	// closed by Close.
	cli *clientv3.Client
}

// New constructs a Store from the supplied clientv3 config. The caller is
// responsible for endpoints and dial timeout: this package deliberately does
// not inject defaults (explicit-over-implicit project rule), so what the caller
// passes is exactly what is used.
func New(ctx context.Context, cfg clientv3.Config) (*Store, error) {
	cli, err := clientv3.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("etcd: new client: %w", err)
	}
	return &Store{cli: cli}, nil
}

// Put stores value at key. When expectedVersion is non-empty the write is a
// compare-and-swap implemented as an etcd Txn comparing the key's ModRevision;
// a precondition mismatch returns store.ErrRevisionConflict and the write does
// not happen. An empty expectedVersion is an unconditional create-or-overwrite.
// The returned RawObject carries the post-write ModRevision as ResourceVersion.
func (s *Store) Put(ctx context.Context, key string, value []byte, expectedVersion string) (store.RawObject, error) {
	txn := s.cli.Txn(ctx)
	if expectedVersion != "" {
		rev, err := strconv.ParseInt(expectedVersion, 10, 64)
		if err != nil {
			// An expectedVersion that is not a valid revision can never equal the
			// stored ModRevision, so this is a guaranteed precondition failure
			// (conflict), not an internal error. This matches the fake's
			// string-comparison CAS semantics and the Task 1 contract, which
			// feeds a non-numeric expectedVersion and requires ErrRevisionConflict.
			return store.RawObject{}, store.ErrRevisionConflict
		}
		txn = txn.If(clientv3.Compare(clientv3.ModRevision(key), "=", rev))
	}

	resp, err := txn.
		Then(clientv3.OpPut(key, string(value))).
		Else(clientv3.OpGet(key)).
		Commit()
	if err != nil {
		return store.RawObject{}, fmt.Errorf("etcd: put txn %q: %w", key, err)
	}

	// A failed CAS means the stored ModRevision did not match the precondition.
	if !resp.Succeeded && expectedVersion != "" {
		return store.RawObject{}, store.ErrRevisionConflict
	}

	// The transaction's header revision is the cluster revision at which the
	// Put committed, which is the ModRevision assigned to this key's new value.
	return store.RawObject{
		Key:             key,
		Value:           append([]byte(nil), value...),
		ResourceVersion: strconv.FormatInt(resp.Header.Revision, 10),
	}, nil
}

// Get returns the object at key or store.ErrNotFound. It uses the clientv3
// default consistency (linearizable), so it never reads stale state.
func (s *Store) Get(ctx context.Context, key string) (store.RawObject, error) {
	resp, err := s.cli.Get(ctx, key)
	if err != nil {
		return store.RawObject{}, fmt.Errorf("etcd: get %q: %w", key, err)
	}
	if len(resp.Kvs) == 0 {
		return store.RawObject{}, store.ErrNotFound
	}
	kv := resp.Kvs[0]
	return store.RawObject{
		Key:             string(kv.Key),
		Value:           append([]byte(nil), kv.Value...),
		ResourceVersion: strconv.FormatInt(kv.ModRevision, 10),
	}, nil
}

// List returns every object whose key starts with prefix, sorted by key. etcd
// returns prefix scans in sorted key order, which satisfies the contract.
func (s *Store) List(ctx context.Context, prefix string) ([]store.RawObject, error) {
	resp, err := s.cli.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("etcd: list %q: %w", prefix, err)
	}
	objs := make([]store.RawObject, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		objs = append(objs, store.RawObject{
			Key:             string(kv.Key),
			Value:           append([]byte(nil), kv.Value...),
			ResourceVersion: strconv.FormatInt(kv.ModRevision, 10),
		})
	}
	return objs, nil
}

// Delete removes key. Deleting a missing key is not an error (idempotent): etcd
// returns success with Deleted == 0 and we surface no error.
func (s *Store) Delete(ctx context.Context, key string) error {
	if _, err := s.cli.Delete(ctx, key); err != nil {
		return fmt.Errorf("etcd: delete %q: %w", key, err)
	}
	return nil
}

// Watch streams events for keys under prefix. With startRevision == "" delivery
// begins from the current revision (changes after this call). With a non-empty
// startRevision, watching resumes from startRevision+1 so the caller does not
// re-receive the change it already observed at startRevision. The returned
// channel is closed when ctx is done (its goroutine then exits), making the
// caller's ctx the sole owner of the watch's lifetime.
func (s *Store) Watch(ctx context.Context, prefix string, startRevision string) (<-chan store.WatchEvent, error) {
	opts := []clientv3.OpOption{clientv3.WithPrefix()}
	if startRevision != "" {
		from, err := strconv.ParseInt(startRevision, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("etcd: parse startRevision %q: %w", startRevision, err)
		}
		opts = append(opts, clientv3.WithRev(from+1))
	}

	watchCh := s.cli.Watch(ctx, prefix, opts...)
	out := make(chan store.WatchEvent)

	go func() {
		// Closing out on exit lets consumers range over the channel and stop
		// cleanly once ctx is cancelled or the watch ends.
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case wresp, ok := <-watchCh:
				if !ok {
					// The clientv3 watch channel closed (ctx cancelled or client
					// closed); terminate the goroutine.
					return
				}
				for _, ev := range wresp.Events {
					out2, ok := translate(ev)
					if !ok {
						continue
					}
					select {
					case out <- out2:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return out, nil
}

// Close releases the underlying etcd client.
func (s *Store) Close() error {
	if err := s.cli.Close(); err != nil {
		return fmt.Errorf("etcd: close client: %w", err)
	}
	return nil
}

// translate converts a clientv3 watch event into a store.WatchEvent. PUT maps
// to ADDED on create and MODIFIED otherwise (IsCreate distinguishes them);
// DELETE maps to DELETED, whose Value may be empty but whose Key is always set.
func translate(ev *clientv3.Event) (store.WatchEvent, bool) {
	switch ev.Type {
	case mvccpb.PUT:
		eventType := store.EventModified
		if ev.IsCreate() {
			eventType = store.EventAdded
		}
		return store.WatchEvent{
			Type: eventType,
			Object: store.RawObject{
				Key:             string(ev.Kv.Key),
				Value:           append([]byte(nil), ev.Kv.Value...),
				ResourceVersion: strconv.FormatInt(ev.Kv.ModRevision, 10),
			},
		}, true
	case mvccpb.DELETE:
		return store.WatchEvent{
			Type: store.EventDeleted,
			Object: store.RawObject{
				Key:             string(ev.Kv.Key),
				ResourceVersion: strconv.FormatInt(ev.Kv.ModRevision, 10),
			},
		}, true
	default:
		return store.WatchEvent{}, false
	}
}
