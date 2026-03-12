package store

import (
	"context"
	"slices"
	"sync"

	"github.com/vibed-project/vibeD/pkg/api"
)

// MemoryStore is an in-memory ArtifactStore for development and testing.
type MemoryStore struct {
	mu        sync.RWMutex
	artifacts map[string]*api.Artifact          // keyed by ID
	byName    map[string]string                 // name -> ID
	versions  map[string][]*api.ArtifactVersion // artifactID -> sorted versions
}

// NewMemoryStore creates a new in-memory artifact store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		artifacts: make(map[string]*api.Artifact),
		byName:    make(map[string]string),
		versions:  make(map[string][]*api.ArtifactVersion),
	}
}

func (s *MemoryStore) Create(_ context.Context, artifact *api.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.byName[artifact.Name]; exists {
		return &api.ErrAlreadyExists{Name: artifact.Name}
	}

	a := *artifact
	s.artifacts[a.ID] = &a
	s.byName[a.Name] = a.ID
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*api.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	a, ok := s.artifacts[id]
	if !ok {
		return nil, &api.ErrNotFound{ArtifactID: id}
	}
	copy := *a
	return &copy, nil
}

func (s *MemoryStore) GetByName(_ context.Context, name string) (*api.Artifact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	id, ok := s.byName[name]
	if !ok {
		return nil, &api.ErrNotFound{ArtifactID: name}
	}
	a := s.artifacts[id]
	copy := *a
	return &copy, nil
}

func (s *MemoryStore) List(_ context.Context, statusFilter string, ownerID string, adminView bool) ([]api.ArtifactSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var summaries []api.ArtifactSummary
	for _, a := range s.artifacts {
		if statusFilter != "" && statusFilter != "all" && string(a.Status) != statusFilter {
			continue
		}
		if !adminView && ownerID != "" {
			isOwner := a.OwnerID == ownerID
			isShared := slices.Contains(a.SharedWith, ownerID)
			if !isOwner && !isShared {
				continue
			}
		}
		summaries = append(summaries, a.ToSummary())
	}
	return summaries, nil
}

func (s *MemoryStore) Update(_ context.Context, artifact *api.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.artifacts[artifact.ID]; !ok {
		return &api.ErrNotFound{ArtifactID: artifact.ID}
	}

	a := *artifact
	s.artifacts[a.ID] = &a
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.artifacts[id]
	if !ok {
		return &api.ErrNotFound{ArtifactID: id}
	}

	delete(s.byName, a.Name)
	delete(s.artifacts, id)
	delete(s.versions, id)
	return nil
}

func (s *MemoryStore) CreateVersion(_ context.Context, version *api.ArtifactVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	v := *version
	s.versions[v.ArtifactID] = append(s.versions[v.ArtifactID], &v)
	return nil
}

func (s *MemoryStore) ListVersions(_ context.Context, artifactID string) ([]api.ArtifactVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	versions := s.versions[artifactID]
	result := make([]api.ArtifactVersion, len(versions))
	for i, v := range versions {
		result[i] = *v
	}
	return result, nil
}

func (s *MemoryStore) GetVersion(_ context.Context, artifactID string, version int) (*api.ArtifactVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, v := range s.versions[artifactID] {
		if v.Version == version {
			copy := *v
			return &copy, nil
		}
	}
	return nil, &api.ErrVersionNotFound{ArtifactID: artifactID, Version: version}
}
