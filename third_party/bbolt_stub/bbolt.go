package bbolt

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Options is a minimal subset of bbolt.Options used by our DataBuffer.
type Options struct {
	Timeout time.Duration
}

// DB is an in-process stub of BoltDB.
// It is NOT a full replacement, but implements the small subset we need for compilation and local testing.
type DB struct {
	path string

	mu      sync.RWMutex
	buckets map[string]map[string][]byte // bucket -> key(string) -> value([]byte)
}

// Open opens (or creates) a database file.
func Open(path string, _ os.FileMode, _ *Options) (*DB, error) {
	db := &DB{
		path:    path,
		buckets: make(map[string]map[string][]byte),
	}
	// Best-effort load: store is optional; if file missing, just start empty.
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if b, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(b)) > 0 {
		var persisted persistedData
		if err := json.Unmarshal(b, &persisted); err == nil {
			for bucket, entries := range persisted.Buckets {
				m := make(map[string][]byte, len(entries))
				for k, v := range entries {
					m[k] = v
				}
				db.buckets[bucket] = m
			}
		}
	} else {
		// Touch file.
		_, _ = os.OpenFile(path, os.O_CREATE, 0o600)
	}
	return db, nil
}

func (db *DB) Close() error {
	db.mu.RLock()
	defer db.mu.RUnlock()

	p := persistedData{Buckets: make(map[string]map[string][]byte, len(db.buckets))}
	for bucket, entries := range db.buckets {
		clone := make(map[string][]byte, len(entries))
		for k, v := range entries {
			clone[k] = append([]byte(nil), v...)
		}
		p.Buckets[bucket] = clone
	}
	b, _ := json.Marshal(p)
	return os.WriteFile(db.path, b, 0o600)
}

func (db *DB) Update(fn func(*Tx) error) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx := &Tx{db: db, writable: true}
	return fn(tx)
}

func (db *DB) View(fn func(*Tx) error) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	tx := &Tx{db: db, writable: false}
	return fn(tx)
}

type persistedData struct {
	Buckets map[string]map[string][]byte `json:"buckets"`
}

type Tx struct {
	db        *DB
	writable  bool
	bucketTxn map[string]Bucket
}

func (tx *Tx) CreateBucketIfNotExists(name []byte) (*Bucket, error) {
	bn := string(name)
	if _, ok := tx.db.buckets[bn]; !ok {
		tx.db.buckets[bn] = make(map[string][]byte)
	}
	return &Bucket{tx: tx, name: bn}, nil
}

func (tx *Tx) Bucket(name []byte) *Bucket {
	bn := string(name)
	if _, ok := tx.db.buckets[bn]; !ok {
		return nil
	}
	return &Bucket{tx: tx, name: bn}
}

type Bucket struct {
	tx   *Tx
	name string
}

func (b *Bucket) Put(key, value []byte) error {
	if !b.tx.writable {
		return errors.New("bbolt stub: Put in read-only tx")
	}
	m := b.tx.db.buckets[b.name]
	m[string(key)] = append([]byte(nil), value...)
	return nil
}

func (b *Bucket) Get(key []byte) []byte {
	m := b.tx.db.buckets[b.name]
	if v, ok := m[string(key)]; ok {
		return append([]byte(nil), v...)
	}
	return nil
}

func (b *Bucket) Delete(key []byte) error {
	if !b.tx.writable {
		return errors.New("bbolt stub: Delete in read-only tx")
	}
	m := b.tx.db.buckets[b.name]
	delete(m, string(key))
	return nil
}

func (b *Bucket) Cursor() *Cursor {
	m := b.tx.db.buckets[b.name]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Lexicographic order matches big-endian time ordering for our 8-byte keys.
	sort.Strings(keys)
	return &Cursor{
		bucket: b,
		keys:   keys,
		idx:    -1,
	}
}

type Cursor struct {
	bucket *Bucket
	keys   []string
	idx    int
}

func (c *Cursor) First() (k, v []byte) {
	if len(c.keys) == 0 {
		return nil, nil
	}
	c.idx = 0
	return c.keyValueAt(c.idx)
}

func (c *Cursor) Next() (k, v []byte) {
	c.idx++
	if c.idx >= len(c.keys) {
		return nil, nil
	}
	return c.keyValueAt(c.idx)
}

func (c *Cursor) keyValueAt(i int) (k, v []byte) {
	m := c.bucket.tx.db.buckets[c.bucket.name]
	keyStr := c.keys[i]
	val := m[keyStr]
	k = []byte(keyStr)
	v = append([]byte(nil), val...)
	return k, v
}

