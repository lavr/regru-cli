package regruzone

import (
	"context"
	"fmt"
	"sync"

	"github.com/lavr/regru-cli/internal/regruapi"
	"github.com/lavr/regru-cli/internal/zone"
)

type Backend struct {
	c    *regruapi.Client
	zone string

	mu        sync.Mutex
	selectors map[zone.Record]Selector
}

func NewBackend(c *regruapi.Client, zoneName string) *Backend {
	return &Backend{c: c, zone: zoneName}
}

func (b *Backend) Identity(ctx context.Context) (string, error) {
	_, id, err := b.c.Nop(ctx)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (b *Backend) Snapshot(ctx context.Context) (zone.Snapshot, string, error) {
	b.mu.Lock()
	b.selectors = nil
	b.mu.Unlock()
	z, err := b.c.GetResourceRecords(ctx, b.zone)
	if err != nil {
		return zone.Snapshot{}, "", err
	}
	o, err := FromAPI(b.zone, z)
	if err != nil {
		return zone.Snapshot{}, "", err
	}
	b.mu.Lock()
	b.selectors = o.Selectors
	b.mu.Unlock()
	return o.Snapshot, o.ServiceID, nil
}

func (b *Backend) Add(ctx context.Context, r zone.Record) error {
	fn, params, err := AddCall(r)
	if err != nil {
		return err
	}
	return b.c.ZoneMutate(ctx, fn, b.zone, params)
}

func (b *Backend) Remove(ctx context.Context, r zone.Record) error {
	b.mu.Lock()
	s, ok := b.selectors[r]
	if ok {
		delete(b.selectors, r)
	}
	b.mu.Unlock()
	if !ok {
		return &Error{CodeValue: "no_selector", Msg: fmt.Sprintf("no observed selector for %s %s %q; read the zone first", r.Name, r.Type, r.Value)}
	}
	fn, params := RemoveCall(r, s)
	return b.c.ZoneMutate(ctx, fn, b.zone, params)
}
