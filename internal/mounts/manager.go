package mounts

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"zipmanga/internal/db"
)

type Manager struct {
	store *db.Store

	mu        sync.RWMutex
	providers map[int64]Provider
}

func NewManager(store *db.Store) *Manager {
	return &Manager{
		store:     store,
		providers: make(map[int64]Provider),
	}
}

func (m *Manager) Reload() error {
	mounts, err := m.store.ListEnabledMounts()
	if err != nil {
		return err
	}

	next := make(map[int64]Provider, len(mounts))
	errMsgs := make([]string, 0)
	for _, mt := range mounts {
		p, err := NewProvider(mt)
		if err != nil {
			errMsgs = append(errMsgs, fmt.Sprintf("mount[%d %s]: %v", mt.ID, mt.Name, err))
			continue
		}
		next[mt.ID] = p
	}

	m.mu.Lock()
	old := m.providers
	m.providers = next
	m.mu.Unlock()

	for _, p := range old {
		_ = p.Close()
	}

	if len(errMsgs) > 0 {
		return errors.New(strings.Join(errMsgs, "; "))
	}
	return nil
}

func NewProvider(mt db.Mount) (Provider, error) {
	roots := normalizeMountRootPaths(mt)
	if len(roots) > 1 {
		return NewMultiRootProvider(mt)
	}
	mt.RootPaths = roots
	mt.RootPath = roots[0]
	return newSingleRootProvider(mt)
}

func (m *Manager) Get(id int64) (Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.providers[id]
	return p, ok
}

func (m *Manager) Ensure(mt db.Mount) (Provider, error) {
	m.mu.RLock()
	if p, ok := m.providers[mt.ID]; ok {
		m.mu.RUnlock()
		return p, nil
	}
	m.mu.RUnlock()

	p, err := NewProvider(mt)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if old, ok := m.providers[mt.ID]; ok {
		m.mu.Unlock()
		_ = p.Close()
		return old, nil
	}
	m.providers[mt.ID] = p
	m.mu.Unlock()
	return p, nil
}

func (m *Manager) List() []Provider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Provider, 0, len(m.providers))
	for _, p := range m.providers {
		out = append(out, p)
	}
	return out
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.providers {
		_ = p.Close()
	}
	m.providers = make(map[int64]Provider)
	return nil
}
