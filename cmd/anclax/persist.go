package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

const (
	storePath        = ".anclax"
	metadataFilename = "metadata.json"
)

func getStorePath(projectDir string) string {
	return filepath.Join(projectDir, storePath)
}

type Metadata struct {
	ExternalVersion map[string]string `json:"external_version"`
}

type Store struct {
	metadata  *Metadata
	storePath string
}

func NewStore(projectDir string) (*Store, error) {
	storePath := getStorePath(projectDir)

	if _, err := os.Stat(storePath); err != nil {
		if os.IsNotExist(err) {
			if err := initStore(storePath); err != nil {
				return nil, errors.Wrap(err, "failed to init store")
			}
		} else {
			return nil, errors.Wrap(err, "failed to check store path")
		}
	}

	metadata, err := readMetadata(storePath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read metadata")
	}

	storePath, err = filepath.Abs(storePath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get absolute path for store path")
	}

	return &Store{
		metadata:  metadata,
		storePath: storePath,
	}, nil
}

func (s *Store) Path() string {
	return s.storePath
}

func (s *Store) Save() error {
	raw, err := json.MarshalIndent(s.metadata, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal metadata")
	}

	return os.WriteFile(filepath.Join(s.storePath, metadataFilename), raw, 0644)
}

func initStore(storePath string) error {
	metadata := &Metadata{
		ExternalVersion: make(map[string]string),
	}

	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return errors.Wrap(err, "failed to marshal metadata")
	}

	if err := os.MkdirAll(storePath, 0755); err != nil {
		return errors.Wrap(err, "failed to create store path")
	}

	return os.WriteFile(filepath.Join(storePath, metadataFilename), raw, 0644)
}

func readMetadata(storePath string) (*Metadata, error) {
	raw, err := os.ReadFile(filepath.Join(storePath, metadataFilename))
	if err != nil {
		return nil, errors.Wrap(err, "failed to read metadata")
	}

	var metadata Metadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal metadata")
	}

	return &metadata, nil
}
