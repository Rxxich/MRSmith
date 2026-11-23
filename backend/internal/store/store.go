// Package store keeps metadata in a single bbolt file as JSON records.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	Users     = []byte("users")
	Sessions  = []byte("sessions")
	Rulesets  = []byte("rulesets")
	Buckets   = []byte("buckets")
	Objects   = []byte("objects")
	Events    = []byte("events")
	Meta      = []byte("meta")
	Access    = []byte("access")
	allTables = [][]byte{Users, Sessions, Rulesets, Buckets, Objects, Events, Meta, Access}
)

// Counter tracks how often a published file was fetched.
type Counter struct {
	Downloads int64 `json:"downloads"`
	// Checks are requests answered "not modified": the client already had
	// the current version.
	Checks int64     `json:"checks"`
	LastAt time.Time `json:"lastAt,omitzero"`
}

func (c *Counter) Add(d Counter) {
	c.Downloads += d.Downloads
	c.Checks += d.Checks
	if d.LastAt.After(c.LastAt) {
		c.LastAt = d.LastAt
	}
}

var ErrNotFound = errors.New("not found")

type DB struct{ db *bolt.DB }

func Open(path string) (*DB, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, t := range allTables {
			if _, err := tx.CreateBucketIfNotExists(t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

// Get decodes the record into v. It returns ErrNotFound when absent.
func (d *DB) Get(table []byte, key string, v any) error {
	return d.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(table).Get([]byte(key))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, v)
	})
}

func (d *DB) Put(table []byte, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(table).Put([]byte(key), raw)
	})
}

// Insert stores v only if key is free.
func (d *DB) Insert(table []byte, key string, v any) (bool, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return false, err
	}
	created := false
	err = d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		if b.Get([]byte(key)) != nil {
			return nil
		}
		created = true
		return b.Put([]byte(key), raw)
	})
	return created, err
}

// Move stores v under a new key and removes the old one, atomically. It
// returns false when the new key is already taken.
func (d *DB) Move(table []byte, from, to string, v any) (bool, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return false, err
	}
	moved := false
	err = d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		if b.Get([]byte(to)) != nil {
			return nil
		}
		if err := b.Put([]byte(to), raw); err != nil {
			return err
		}
		moved = true
		return b.Delete([]byte(from))
	})
	return moved, err
}

func (d *DB) Delete(table []byte, key string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(table).Delete([]byte(key))
	})
}

// DeletePrefix removes every key that starts with prefix and returns the
// removed raw values.
func (d *DB) DeletePrefix(table []byte, prefix string) ([][]byte, error) {
	var removed [][]byte
	err := d.db.Update(func(tx *bolt.Tx) error {
		c := tx.Bucket(table).Cursor()
		p := []byte(prefix)
		var keys [][]byte
		for k, v := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, v = c.Next() {
			keys = append(keys, bytes.Clone(k))
			removed = append(removed, bytes.Clone(v))
		}
		b := tx.Bucket(table)
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
	return removed, err
}

// Scan calls fn for each record whose key starts with prefix, in key order.
// Returning errStop from fn ends the scan early without error.
func (d *DB) Scan(table []byte, prefix string, fn func(key string, raw []byte) error) error {
	return d.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(table).Cursor()
		p := []byte(prefix)
		for k, v := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, v = c.Next() {
			if err := fn(string(k), v); err != nil {
				if errors.Is(err, ErrStop) {
					return nil
				}
				return err
			}
		}
		return nil
	})
}

// ScanReverse iterates the whole table from the last key backwards.
func (d *DB) ScanReverse(table []byte, fn func(key string, raw []byte) error) error {
	return d.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(table).Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			if err := fn(string(k), v); err != nil {
				if errors.Is(err, ErrStop) {
					return nil
				}
				return err
			}
		}
		return nil
	})
}

// Count returns the number of records in a table.
func (d *DB) Count(table []byte) int {
	n := 0
	_ = d.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(table).Stats().KeyN
		return nil
	})
	return n
}

// TrimOldest keeps at most max records in a table, dropping the lowest keys.
func (d *DB) TrimOldest(table []byte, max int) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		extra := b.Stats().KeyN - max
		if extra <= 0 {
			return nil
		}
		c := b.Cursor()
		var keys [][]byte
		for k, _ := c.First(); k != nil && len(keys) < extra; k, _ = c.Next() {
			keys = append(keys, bytes.Clone(k))
		}
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

var ErrStop = errors.New("stop")

// Modify runs a read-modify-write on one record inside a single transaction.
func Modify[T any](d *DB, table []byte, key string, fn func(v *T) error) (*T, error) {
	var out *T
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		raw := b.Get([]byte(key))
		if raw == nil {
			return ErrNotFound
		}
		v := new(T)
		if err := json.Unmarshal(raw, v); err != nil {
			return err
		}
		if err := fn(v); err != nil {
			return err
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out = v
		return b.Put([]byte(key), enc)
	})
	return out, err
}

// Upsert stores what fn returns for the current record (nil when absent),
// reading and writing in one transaction.
func Upsert[T any](d *DB, table []byte, key string, fn func(old *T) (*T, error)) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		var old *T
		if raw := b.Get([]byte(key)); raw != nil {
			old = new(T)
			if err := json.Unmarshal(raw, old); err != nil {
				return err
			}
		}
		v, err := fn(old)
		if err != nil {
			return err
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return b.Put([]byte(key), enc)
	})
}

// Rekey moves a record to a new key, applying fn on the way, in one
// transaction. It returns ErrNotFound when from is missing and false when
// to is already taken.
func Rekey[T any](d *DB, table []byte, from, to string, fn func(v *T)) (*T, bool, error) {
	var out *T
	moved := false
	err := d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		raw := b.Get([]byte(from))
		if raw == nil {
			return ErrNotFound
		}
		if b.Get([]byte(to)) != nil {
			return nil
		}
		v := new(T)
		if err := json.Unmarshal(raw, v); err != nil {
			return err
		}
		fn(v)
		enc, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(to), enc); err != nil {
			return err
		}
		out, moved = v, true
		return b.Delete([]byte(from))
	})
	return out, moved, err
}

// ModifyEach runs fn on each existing record among keys, all in one
// transaction. Missing keys are skipped.
func ModifyEach[T any](d *DB, table []byte, keys []string, fn func(key string, v *T)) error {
	if len(keys) == 0 {
		return nil
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		for _, k := range keys {
			raw := b.Get([]byte(k))
			if raw == nil {
				continue
			}
			v := new(T)
			if err := json.Unmarshal(raw, v); err != nil {
				return err
			}
			fn(k, v)
			enc, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if err := b.Put([]byte(k), enc); err != nil {
				return err
			}
		}
		return nil
	})
}

// PutMany stores several records in one transaction.
func (d *DB) PutMany(table []byte, items map[string]any) error {
	if len(items) == 0 {
		return nil
	}
	enc := make(map[string][]byte, len(items))
	for k, v := range items {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		enc[k] = raw
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(table)
		for k, raw := range enc {
			if err := b.Put([]byte(k), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReadTables calls fn for every record of the given tables inside one read
// transaction, so together they form a consistent snapshot. raw is only
// valid during the call.
func (d *DB) ReadTables(tables [][]byte, fn func(table []byte, key string, raw []byte) error) error {
	return d.db.View(func(tx *bolt.Tx) error {
		for _, t := range tables {
			if err := tx.Bucket(t).ForEach(func(k, v []byte) error { return fn(t, string(k), v) }); err != nil {
				return err
			}
		}
		return nil
	})
}

// Record is a key with its encoded value.
type Record struct {
	Key string
	Raw []byte
}

// ReplaceTables empties each table named in data and fills it with the given
// records, all in one transaction. dropped, when set, sees every record
// removed on the way; its raw is only valid during the call.
func (d *DB) ReplaceTables(data map[string][]Record, dropped func(table, key string, raw []byte)) error {
	for name := range data {
		if !slices.ContainsFunc(allTables, func(t []byte) bool { return string(t) == name }) {
			return errors.New("unknown table " + name)
		}
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		for name, recs := range data {
			t := []byte(name)
			if dropped != nil {
				if err := tx.Bucket(t).ForEach(func(k, v []byte) error {
					dropped(name, string(k), v)
					return nil
				}); err != nil {
					return err
				}
			}
			if err := tx.DeleteBucket(t); err != nil {
				return err
			}
			b, err := tx.CreateBucket(t)
			if err != nil {
				return err
			}
			for _, r := range recs {
				if err := b.Put([]byte(r.Key), r.Raw); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// List decodes every record of a table.
func List[T any](d *DB, table []byte, prefix string) ([]T, error) {
	var out []T
	err := d.Scan(table, prefix, func(_ string, raw []byte) error {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		out = append(out, v)
		return nil
	})
	return out, err
}

// NewID returns 128 random bits as hex.
func NewID() string { return RandomHex(16) }

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// IsID reports whether s has the shape of an ID produced by NewID.
func IsID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// TimeKey builds a sortable key for time-ordered records.
func TimeKey(t time.Time) string {
	b := make([]byte, 8)
	n := uint64(t.UnixNano())
	for i := 7; i >= 0; i-- {
		b[i] = byte(n)
		n >>= 8
	}
	return hex.EncodeToString(b) + RandomHex(4)
}
