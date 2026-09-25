package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"
)

// Memory is a Store that keeps objects in the process. It stands in for the bucket in
// development without one configured and in the integration suite, which reads back what
// was written through Object.
type Memory struct {
	mu      sync.Mutex
	objects map[string]MemoryObject
}

// MemoryObject is one stored object as Memory holds it.
type MemoryObject struct {
	ContentType string
	Body        []byte
}

func NewMemory() *Memory {
	return &Memory{objects: map[string]MemoryObject{}}
}

func (m *Memory) Put(_ context.Context, key, contentType string, body io.Reader, _ int64) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, body); err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = MemoryObject{ContentType: contentType, Body: buf.Bytes()}
	return nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// SignedURL returns a recognisable fake: memory objects cannot be fetched over HTTP.
func (m *Memory) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	return "memory://" + url.PathEscape(key) + "?ttl=" + ttl.String(), nil
}

// Object returns what is stored at key.
func (m *Memory) Object(key string) (MemoryObject, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	return obj, ok
}

// Keys lists every stored key, for assertions that something was cleaned up.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for key := range m.objects {
		keys = append(keys, key)
	}
	return keys
}
