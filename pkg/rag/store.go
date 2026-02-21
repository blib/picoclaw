package rag

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketMeta   = []byte("meta")
	bucketChunks = []byte("chunks")
	keyInfo      = []byte("info")
	keyDirty     = []byte("dirty")
)

// Store persists RAG index data in a bbolt database (chunks + metadata)
// and a flat binary file (vectors). This replaces the JSON-based storage
// for better write performance and smaller on-disk footprint.
type Store struct {
	dir string
	db  *bolt.DB
}

// OpenStore opens or creates the bbolt database in dir.
func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(dir, "index.db"), 0o644, &bolt.Options{
		NoSync: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	return &Store{dir: dir, db: db}, nil
}

func (s *Store) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// SetDirty marks the index as dirty (in-memory state is ahead of disk).
// On startup, if dirty is true the index must be rebuilt from source files.
// Syncs to disk immediately so the flag survives crashes.
func (s *Store) SetDirty(dirty bool) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		if dirty {
			return b.Put(keyDirty, []byte{1})
		}
		return b.Delete(keyDirty)
	})
	if err != nil {
		return err
	}
	return s.db.Sync()
}

// IsDirty returns true if the index was not cleanly flushed.
func (s *Store) IsDirty() bool {
	var dirty bool
	_ = s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return nil
		}
		dirty = b.Get(keyDirty) != nil
		return nil
	})
	return dirty
}

// SaveIndex writes index metadata and all chunks in a single bbolt transaction.
// Syncs to disk so the data survives crashes (bbolt runs with NoSync for
// performance; explicit Sync at commit boundaries provides durability).
func (s *Store) SaveIndex(info IndexInfo, chunks []IndexedChunk) error {
	err := s.db.Update(func(tx *bolt.Tx) error {
		mb, err := tx.CreateBucketIfNotExists(bucketMeta)
		if err != nil {
			return err
		}
		infoData, err := json.Marshal(info)
		if err != nil {
			return err
		}
		if err := mb.Put(keyInfo, infoData); err != nil {
			return err
		}

		// recreate chunks bucket to clear stale data
		if err := tx.DeleteBucket(bucketChunks); err != nil && err != bolt.ErrBucketNotFound {
			return fmt.Errorf("delete chunks bucket: %w", err)
		}
		cb, err := tx.CreateBucket(bucketChunks)
		if err != nil {
			return err
		}
		for i, c := range chunks {
			data, err := json.Marshal(c)
			if err != nil {
				return fmt.Errorf("marshal chunk %d: %w", i, err)
			}
			key := make([]byte, 4)
			binary.BigEndian.PutUint32(key, uint32(i))
			if err := cb.Put(key, data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.db.Sync()
}

// LoadIndexInfo reads only the index metadata without loading chunks.
func (s *Store) LoadIndexInfo() (*IndexInfo, error) {
	var info IndexInfo
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if b == nil {
			return ErrIndexNotBuilt
		}
		data := b.Get(keyInfo)
		if data == nil {
			return ErrIndexNotBuilt
		}
		return json.Unmarshal(data, &info)
	})
	if err != nil {
		return nil, err
	}
	return &info, nil
}

// LoadChunks reads all indexed chunks in insertion order.
func (s *Store) LoadChunks() ([]IndexedChunk, error) {
	var chunks []IndexedChunk
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var c IndexedChunk
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			chunks = append(chunks, c)
			return nil
		})
	})
	return chunks, err
}

// vectors.bin format v1:
//
//	[4B magic "PCVF"][2B version LE][2B reserved]
//	[4B count LE][4B dims LE]
//	[count * dims * 4B float32 LE]
//	[4B CRC32-C of everything above]
//
// Total overhead: 20 bytes (header 8 + count/dims 8 + trailer 4).
var (
	vecMagic   = [4]byte{'P', 'C', 'V', 'F'} // PicoClaw Vector File
	vecVersion = uint16(1)
)

const vecHeaderSize = 16 // magic(4) + version(2) + reserved(2) + count(4) + dims(4)
const vecTrailerSize = 4 // CRC32-C

// SaveVectors writes embedding vectors as a flat binary file with a
// magic header and CRC32-C trailer for corruption detection.
func (s *Store) SaveVectors(vectors [][]float32) error {
	if len(vectors) == 0 {
		os.Remove(s.vectorsPath())
		return nil
	}
	dims := len(vectors[0])
	payloadSize := len(vectors) * dims * 4
	buf := make([]byte, vecHeaderSize+payloadSize+vecTrailerSize)

	// header
	copy(buf[0:4], vecMagic[:])
	binary.LittleEndian.PutUint16(buf[4:6], vecVersion)
	// buf[6:8] reserved, zero
	binary.LittleEndian.PutUint32(buf[8:12], uint32(len(vectors)))
	binary.LittleEndian.PutUint32(buf[12:16], uint32(dims))

	// payload
	off := vecHeaderSize
	for _, vec := range vectors {
		for _, v := range vec {
			binary.LittleEndian.PutUint32(buf[off:off+4], math.Float32bits(v))
			off += 4
		}
	}

	// trailer: CRC32-C over header + payload
	checksum := crc32.Checksum(buf[:off], crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(buf[off:off+4], checksum)

	return os.WriteFile(s.vectorsPath(), buf, 0o644)
}

// LoadVectors reads the binary vector file. Returns nil, nil if the file
// doesn't exist (no embeddings were stored).
func (s *Store) LoadVectors() ([][]float32, error) {
	data, err := os.ReadFile(s.vectorsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) < vecHeaderSize+vecTrailerSize {
		return nil, fmt.Errorf("vectors.bin too short (%d bytes)", len(data))
	}

	// validate magic
	if data[0] != vecMagic[0] || data[1] != vecMagic[1] ||
		data[2] != vecMagic[2] || data[3] != vecMagic[3] {
		return nil, fmt.Errorf("vectors.bin bad magic: %x", data[0:4])
	}

	ver := binary.LittleEndian.Uint16(data[4:6])
	if ver != vecVersion {
		return nil, fmt.Errorf("vectors.bin unsupported version %d", ver)
	}

	n := int(binary.LittleEndian.Uint32(data[8:12]))
	dims := int(binary.LittleEndian.Uint32(data[12:16]))
	expected := vecHeaderSize + n*dims*4 + vecTrailerSize
	if len(data) < expected {
		return nil, fmt.Errorf("vectors.bin truncated: want %d, got %d bytes", expected, len(data))
	}

	// validate CRC32-C
	payloadEnd := vecHeaderSize + n*dims*4
	stored := binary.LittleEndian.Uint32(data[payloadEnd : payloadEnd+4])
	computed := crc32.Checksum(data[:payloadEnd], crc32.MakeTable(crc32.Castagnoli))
	if stored != computed {
		return nil, fmt.Errorf("vectors.bin checksum mismatch: stored %08x, computed %08x", stored, computed)
	}

	vectors := make([][]float32, n)
	off := vecHeaderSize
	for i := range vectors {
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = math.Float32frombits(binary.LittleEndian.Uint32(data[off : off+4]))
			off += 4
		}
		vectors[i] = vec
	}
	return vectors, nil
}

func (s *Store) vectorsPath() string {
	return filepath.Join(s.dir, "vectors.bin")
}

// errStopIteration is a sentinel used to break out of bbolt's ForEach early.
var errStopIteration = errors.New("stop iteration")

// ForEachChunk iterates all chunks in insertion order without accumulating
// them in memory. The callback receives the positional index and chunk.
func (s *Store) ForEachChunk(fn func(idx uint32, chunk IndexedChunk) error) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			idx := binary.BigEndian.Uint32(k)
			var c IndexedChunk
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			return fn(idx, c)
		})
	})
}

// ForEachVector iterates all vectors without keeping the full [][]float32
// in memory. Each callback receives a freshly-allocated []float32 that the
// caller may retain. Returns nil if the vectors file doesn't exist.
func (s *Store) ForEachVector(fn func(idx uint32, vec []float32) error) error {
	data, err := os.ReadFile(s.vectorsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) < vecHeaderSize+vecTrailerSize {
		return fmt.Errorf("vectors.bin too short (%d bytes)", len(data))
	}
	if data[0] != vecMagic[0] || data[1] != vecMagic[1] ||
		data[2] != vecMagic[2] || data[3] != vecMagic[3] {
		return fmt.Errorf("vectors.bin bad magic: %x", data[0:4])
	}
	ver := binary.LittleEndian.Uint16(data[4:6])
	if ver != vecVersion {
		return fmt.Errorf("vectors.bin unsupported version %d", ver)
	}
	n := int(binary.LittleEndian.Uint32(data[8:12]))
	dims := int(binary.LittleEndian.Uint32(data[12:16]))
	expected := vecHeaderSize + n*dims*4 + vecTrailerSize
	if len(data) < expected {
		return fmt.Errorf("vectors.bin truncated: want %d, got %d bytes", expected, len(data))
	}
	payloadEnd := vecHeaderSize + n*dims*4
	stored := binary.LittleEndian.Uint32(data[payloadEnd : payloadEnd+4])
	computed := crc32.Checksum(data[:payloadEnd], crc32.MakeTable(crc32.Castagnoli))
	if stored != computed {
		return fmt.Errorf("vectors.bin checksum mismatch: stored %08x, computed %08x", stored, computed)
	}
	off := vecHeaderSize
	for i := 0; i < n; i++ {
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = math.Float32frombits(binary.LittleEndian.Uint32(data[off : off+4]))
			off += 4
		}
		if err := fn(uint32(i), vec); err != nil {
			return err
		}
	}
	return nil
}

// LoadChunksByIndexes reads specific chunks by their positional indexes
// in a single bbolt transaction (batch read for search hit resolution).
func (s *Store) LoadChunksByIndexes(ids []uint32) (map[uint32]IndexedChunk, error) {
	result := make(map[uint32]IndexedChunk, len(ids))
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return nil
		}
		key := make([]byte, 4)
		for _, id := range ids {
			binary.BigEndian.PutUint32(key, id)
			v := b.Get(key)
			if v == nil {
				continue
			}
			var c IndexedChunk
			if err := json.Unmarshal(v, &c); err != nil {
				return fmt.Errorf("unmarshal chunk %d: %w", id, err)
			}
			result[id] = c
		}
		return nil
	})
	return result, err
}

// LoadChunkBySourceAndOrdinal finds a chunk by source path and ordinal.
// Scans all chunks via bbolt cursor — O(n) but avoids keeping chunks in memory.
func (s *Store) LoadChunkBySourceAndOrdinal(sourcePath string, ordinal int) (*IndexedChunk, error) {
	norm := filepath.ToSlash(sourcePath)
	var found *IndexedChunk
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketChunks)
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var c IndexedChunk
			if err := json.Unmarshal(v, &c); err != nil {
				return err
			}
			if c.SourcePath == norm && c.ChunkOrdinal == ordinal {
				found = &c
				return errStopIteration
			}
			return nil
		})
	})
	if err == errStopIteration {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, os.ErrNotExist
	}
	return found, nil
}
