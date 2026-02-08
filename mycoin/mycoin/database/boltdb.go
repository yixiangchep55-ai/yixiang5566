package database

import (
	"fmt"
	"log"

	bolt "go.etcd.io/bbolt"
)

type BoltDB struct {
	DB *bolt.DB
}

func OpenDB(path string) *BoltDB {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		log.Fatal(err)
	}

	// 创建 bucket
	db.Update(func(tx *bolt.Tx) error {
		tx.CreateBucketIfNotExists([]byte("blocks"))
		tx.CreateBucketIfNotExists([]byte("index"))
		tx.CreateBucketIfNotExists([]byte("utxo"))
		tx.CreateBucketIfNotExists([]byte("meta"))
		tx.CreateBucketIfNotExists([]byte("txindex"))
		tx.CreateBucketIfNotExists([]byte("mempool"))
		tx.CreateBucketIfNotExists([]byte("peerstore"))
		return nil
	})

	return &BoltDB{DB: db}
}

func (db *BoltDB) Put(bucket, key string, value []byte) error {
	return db.DB.Update(func(tx *bolt.Tx) error {

		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return err
		}

		return b.Put([]byte(key), value)
	})
}

func (b *BoltDB) Get(bucket string, key string) []byte {
	var val []byte
	b.DB.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(bucket)).Get([]byte(key))
		if v != nil {
			val = append([]byte{}, v...)
		}
		return nil
	})
	return val
}

func (b *BoltDB) Delete(bucket string, key string) {
	b.DB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(bucket)).Delete([]byte(key))
	})
}

func (db *BoltDB) Iterate(bucket string, fn func(k, v []byte)) error {
	return db.DB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s not found", bucket)
		}

		return b.ForEach(func(k, v []byte) error {
			fn(k, v)
			return nil
		})
	})
}

func (db *BoltDB) ClearBucket(bucket string) error {
	return db.DB.Update(func(tx *bolt.Tx) error {
		err := tx.DeleteBucket([]byte(bucket))
		if err != nil && err != bolt.ErrBucketNotFound {
			return err
		}
		_, err = tx.CreateBucket([]byte(bucket))
		return err
	})
}
