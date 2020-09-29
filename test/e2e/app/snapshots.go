// nolint: gosec
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path/filepath"

	abci "github.com/tendermint/tendermint/abci/types"
)

const (
	snapshotChunkSize = 1e6 // bytes
)

type SnapshotStore struct {
	dir      string
	metadata []abci.Snapshot
}

func NewSnapshotStore(dir string) (*SnapshotStore, error) {
	store := &SnapshotStore{dir: dir}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := store.loadMetadata(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SnapshotStore) Create(state *State) (abci.Snapshot, error) {
	bz, err := state.Export()
	if err != nil {
		return abci.Snapshot{}, err
	}
	hash := sha256.Sum256(bz)
	snapshot := abci.Snapshot{
		Height: state.Height,
		Format: 1,
		Hash:   hash[:],
		Chunks: s.chunks(bz),
	}
	err = ioutil.WriteFile(filepath.Join(s.dir, fmt.Sprintf("%v.json", state.Height)), bz, 0644)
	if err != nil {
		return abci.Snapshot{}, err
	}
	s.metadata = append(s.metadata, snapshot)
	err = s.saveMetadata()
	if err != nil {
		return abci.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *SnapshotStore) List() ([]*abci.Snapshot, error) {
	snapshots := []*abci.Snapshot{}
	for _, snapshot := range s.metadata {
		s := snapshot // copy to avoid pointer to range variable
		snapshots = append(snapshots, &s)
	}
	return snapshots, nil
}

func (s *SnapshotStore) LoadChunk(height uint64, format uint32, chunk uint32) ([]byte, error) {
	for _, snapshot := range s.metadata {
		if snapshot.Height == height && snapshot.Format == format {
			bz, err := ioutil.ReadFile(filepath.Join(s.dir, fmt.Sprintf("%v.json", height)))
			if err != nil {
				return nil, err
			}
			return s.chunk(bz, chunk), nil
		}
	}
	return nil, nil
}

func (s *SnapshotStore) chunk(bz []byte, index uint32) []byte {
	start := int(index * snapshotChunkSize)
	end := int((index + 1) * snapshotChunkSize)
	switch {
	case start >= len(bz):
		return nil
	case end >= len(bz):
		return bz[start:]
	default:
		return bz[start:end]
	}
}

func (s *SnapshotStore) chunks(bz []byte) uint32 {
	return uint32(math.Ceil(float64(len(bz)) / snapshotChunkSize))
}

func (s *SnapshotStore) loadMetadata() error {
	file := filepath.Join(s.dir, "metadata.json")
	metadata := []abci.Snapshot{}

	bz, err := ioutil.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("failed to load snapshot metadata from %q: %w", file, err)
	}
	if len(bz) != 0 {
		err = json.Unmarshal(bz, &metadata)
		if err != nil {
			return fmt.Errorf("invalid snapshot data in %q: %w", file, err)
		}
	}
	s.metadata = metadata
	return nil
}

func (s *SnapshotStore) saveMetadata() error {
	file := filepath.Join(s.dir, "metadata.json")
	bz, err := json.Marshal(s.metadata)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(file, bz, 0644) // nolint: gosec
}
