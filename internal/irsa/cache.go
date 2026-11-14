package irsa

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Cache keeps responses on disk and merges concurrent requests for the same
// key, so a slow TAP query runs once even if several tabs ask for it.
type Cache struct {
	Dir string

	mu       sync.Mutex
	inflight map[string]*call
}

type call struct {
	done chan struct{}
	data []byte
	err  error
}

func NewCache(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Cache{Dir: dir, inflight: map[string]*call{}}, nil
}

func (c *Cache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(c.Dir, h[:2], h)
}

// Get returns the cached value if it is younger than ttl (0 means forever),
// otherwise it runs fetch and stores the result. hit reports a cache hit.
func (c *Cache) Get(key string, ttl time.Duration, fetch func() ([]byte, error)) (data []byte, hit bool, err error) {
	p := c.path(key)
	if st, err := os.Stat(p); err == nil && (ttl == 0 || time.Since(st.ModTime()) < ttl) {
		if data, err := os.ReadFile(p); err == nil {
			return data, true, nil
		}
	}

	c.mu.Lock()
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.data, false, cl.err
	}
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()

	cl.data, cl.err = fetch()
	if cl.err == nil {
		c.store(p, cl.data)
	}
	c.mu.Lock()
	delete(c.inflight, key)
	c.mu.Unlock()
	close(cl.done)
	return cl.data, false, cl.err
}

// Lookup returns a cached value younger than ttl (0 means forever).
func (c *Cache) Lookup(key string, ttl time.Duration) ([]byte, bool) {
	p := c.path(key)
	if st, err := os.Stat(p); err == nil && (ttl == 0 || time.Since(st.ModTime()) < ttl) {
		if data, err := os.ReadFile(p); err == nil {
			return data, true
		}
	}
	return nil, false
}

// Put stores a value, for answers that are only worth caching when complete.
func (c *Cache) Put(key string, data []byte) {
	c.store(c.path(key), data)
}

// store writes atomically so a crash never leaves half a file in the cache.
func (c *Cache) store(p string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
	}
}
