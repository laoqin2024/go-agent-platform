package buffer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const defaultBucket = "agent_cache"
const (
	defaultMaxDBSizeBytes = int64(1 << 30) // 1GB
	pruneTargetRatio      = 0.90
	pruneChunkSize        = 200
)

// BufferRecord is one stored item in the buffer.
type BufferRecord struct {
	Key      []byte
	DataType string
	Payload  []byte
}

// DataBuffer buffers outbound data to prevent data loss during network interruptions.
// It stores records in a BoltDB bucket ordered by time (big-endian UnixNano keys).
type DataBuffer struct {
	db   *bolt.DB
	path string

	// Serialize writers. bbolt allows multiple readers but only one writer;
	// this mutex keeps our Save() calls simple and deterministic.
	writeMu sync.Mutex
	dbMu    sync.RWMutex

	maxDBSizeBytes int64
}

func NewDataBuffer(dbPath string) (*DataBuffer, error) {
	if dbPath == "" {
		return nil, errors.New("buffer: empty db path")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("buffer: create data dir: %w", err)
	}

	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	return &DataBuffer{
		db:             db,
		path:           dbPath,
		maxDBSizeBytes: defaultMaxDBSizeBytes,
	}, nil
}

func (b *DataBuffer) Close() error {
	b.dbMu.Lock()
	defer b.dbMu.Unlock()
	if b.db == nil {
		return nil
	}
	return b.db.Close()
}

// Save stores the given payload under a time-ordered key.
// DataType is stored inside the value so PeekBatch can decode it without knowing the bucket.
func (b *DataBuffer) Save(dataType string, payload []byte) error {
	if dataType == "" {
		return errors.New("buffer: empty dataType")
	}
	b.dbMu.RLock()
	db := b.db
	if db == nil {
		b.dbMu.RUnlock()
		return errors.New("buffer: db not initialized")
	}

	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(time.Now().UnixNano()))

	// Value encoding:
	// [2-byte big-endian dataTypeLen][dataType bytes][payload bytes]
	if len(dataType) > 0xffff {
		return errors.New("buffer: dataType too long")
	}
	val := make([]byte, 2+len(dataType)+len(payload))
	binary.BigEndian.PutUint16(val[:2], uint16(len(dataType)))
	copy(val[2:2+len(dataType)], []byte(dataType))
	copy(val[2+len(dataType):], payload)

	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(defaultBucket))
		if err != nil {
			return err
		}
		return bucket.Put(key, val)
	}); err != nil {
		b.dbMu.RUnlock()
		return err
	}
	b.dbMu.RUnlock()
	return b.enforceSizePolicyLocked()
}

// PeekBatch reads the earliest N records without deleting them.
func (b *DataBuffer) PeekBatch(limit int) ([]BufferRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	b.dbMu.RLock()
	db := b.db
	if db == nil {
		b.dbMu.RUnlock()
		return nil, errors.New("buffer: db not initialized")
	}

	var out []BufferRecord
	err := db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(defaultBucket))
		if bucket == nil {
			return nil
		}
		c := bucket.Cursor()
		k, v := c.First()
		for i := 0; i < limit && k != nil; i++ {
			dataType, payload, decErr := decodeValue(v)
			if decErr != nil {
				// Skip invalid records rather than blocking.
				k, v = c.Next()
				continue
			}

			// Copy keys/payload to detach from transaction memory.
			keyCopy := append([]byte(nil), k...)
			payloadCopy := append([]byte(nil), payload...)
			out = append(out, BufferRecord{
				Key:      keyCopy,
				DataType: dataType,
				Payload:  payloadCopy,
			})
			k, v = c.Next()
		}
		return nil
	})
	b.dbMu.RUnlock()
	return out, err
}

// DeleteBatch deletes all records by their keys (as returned by PeekBatch).
func (b *DataBuffer) DeleteBatch(keys [][]byte) error {
	if len(keys) == 0 {
		return nil
	}
	b.dbMu.RLock()
	db := b.db
	if db == nil {
		b.dbMu.RUnlock()
		return errors.New("buffer: db not initialized")
	}

	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	if err := db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(defaultBucket))
		if bucket == nil {
			return nil
		}
		for _, k := range keys {
			if len(k) == 0 {
				continue
			}
			// Keys are expected to be the raw BoltDB keys.
			if err := bucket.Delete(k); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.dbMu.RUnlock()
		return err
	}
	b.dbMu.RUnlock()
	return b.enforceSizePolicyLocked()
}

func (b *DataBuffer) enforceSizePolicyLocked() error {
	if b.maxDBSizeBytes <= 0 {
		return nil
	}
	size, err := b.fileSizeBytes()
	if err != nil || size <= b.maxDBSizeBytes {
		return err
	}
	return b.pruneOldestToTargetLocked()
}

func (b *DataBuffer) fileSizeBytes() (int64, error) {
	fi, err := os.Stat(b.path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (b *DataBuffer) pruneOldestToTargetLocked() error {
	target := int64(float64(b.maxDBSizeBytes) * pruneTargetRatio)
	if target <= 0 {
		target = b.maxDBSizeBytes
	}
	for {
		size, err := b.fileSizeBytes()
		if err != nil {
			return err
		}
		if size <= target {
			return nil
		}
		deleted, err := b.deleteOldestChunk(pruneChunkSize)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
	}
}

func (b *DataBuffer) deleteOldestChunk(n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	deleted := 0
	err := b.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(defaultBucket))
		if bucket == nil {
			return nil
		}
		c := bucket.Cursor()
		k, _ := c.First()
		for i := 0; i < n && k != nil; i++ {
			keyCopy := append([]byte(nil), k...)
			if err := bucket.Delete(keyCopy); err != nil {
				return err
			}
			deleted++
			k, _ = c.Next()
		}
		return nil
	})
	return deleted, err
}

func decodeValue(v []byte) (dataType string, payload []byte, err error) {
	if len(v) < 2 {
		return "", nil, errors.New("buffer: invalid value")
	}
	dl := binary.BigEndian.Uint16(v[:2])
	if int(2+dl) > len(v) {
		return "", nil, errors.New("buffer: invalid value")
	}
	dataType = string(v[2 : 2+dl])
	payload = v[2+dl:]
	return dataType, payload, nil
}
