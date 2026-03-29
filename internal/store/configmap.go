package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/vibed-project/vibeD/pkg/api"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ConfigMapStore persists artifact metadata in a Kubernetes ConfigMap.
// Each artifact is stored as a JSON entry keyed by its ID.
// Versions are stored in a separate ConfigMap named "{name}-versions".
type ConfigMapStore struct {
	client       kubernetes.Interface
	name         string
	versionsName string // e.g. "vibed-artifacts-versions"
	namespace    string
	mu           sync.Mutex
}

// NewConfigMapStore creates a ConfigMap-backed artifact store.
func NewConfigMapStore(client kubernetes.Interface, name, namespace string) *ConfigMapStore {
	return &ConfigMapStore{
		client:       client,
		name:         name,
		versionsName: name + "-versions",
		namespace:    namespace,
	}
}

func (s *ConfigMapStore) Create(ctx context.Context, artifact *api.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getOrCreateConfigMap(ctx, s.name)
	if err != nil {
		return err
	}

	// Check for name collision
	for _, v := range cm.Data {
		var existing api.Artifact
		if json.Unmarshal([]byte(v), &existing) == nil && existing.Name == artifact.Name {
			return &api.ErrAlreadyExists{Name: artifact.Name}
		}
	}

	data, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("marshaling artifact: %w", err)
	}

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[artifact.ID] = string(data)

	return s.updateConfigMap(ctx, cm)
}

func (s *ConfigMapStore) Get(ctx context.Context, id string) (*api.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.name)
	if err != nil {
		return nil, err
	}

	data, ok := cm.Data[id]
	if !ok {
		return nil, &api.ErrNotFound{ArtifactID: id}
	}

	var artifact api.Artifact
	if err := json.Unmarshal([]byte(data), &artifact); err != nil {
		return nil, fmt.Errorf("unmarshaling artifact: %w", err)
	}
	return &artifact, nil
}

func (s *ConfigMapStore) GetByName(ctx context.Context, name string) (*api.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.name)
	if err != nil {
		return nil, err
	}

	for _, v := range cm.Data {
		var artifact api.Artifact
		if json.Unmarshal([]byte(v), &artifact) == nil && artifact.Name == name {
			return &artifact, nil
		}
	}
	return nil, &api.ErrNotFound{ArtifactID: name}
}

func (s *ConfigMapStore) List(ctx context.Context, opts ListOptions) (*ListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return &ListResult{}, nil
		}
		return nil, err
	}

	var summaries []api.ArtifactSummary
	for _, v := range cm.Data {
		var artifact api.Artifact
		if err := json.Unmarshal([]byte(v), &artifact); err != nil {
			continue
		}
		if opts.StatusFilter != "" && opts.StatusFilter != "all" && string(artifact.Status) != opts.StatusFilter {
			continue
		}
		if !opts.AdminView && opts.OwnerID != "" {
			isOwner := artifact.OwnerID == opts.OwnerID
			isShared := slices.Contains(artifact.SharedWith, opts.OwnerID)
			if !isOwner && !isShared {
				continue
			}
		}
		summaries = append(summaries, artifact.ToSummary())
	}

	total := len(summaries)

	if opts.Offset > 0 && opts.Offset < len(summaries) {
		summaries = summaries[opts.Offset:]
	} else if opts.Offset >= len(summaries) {
		summaries = nil
	}

	if opts.Limit > 0 && opts.Limit < len(summaries) {
		summaries = summaries[:opts.Limit]
	}

	return &ListResult{Artifacts: summaries, Total: total}, nil
}

func (s *ConfigMapStore) Update(ctx context.Context, artifact *api.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.name)
	if err != nil {
		return err
	}

	if _, ok := cm.Data[artifact.ID]; !ok {
		return &api.ErrNotFound{ArtifactID: artifact.ID}
	}

	data, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("marshaling artifact: %w", err)
	}

	cm.Data[artifact.ID] = string(data)
	return s.updateConfigMap(ctx, cm)
}

func (s *ConfigMapStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.name)
	if err != nil {
		return err
	}

	if _, ok := cm.Data[id]; !ok {
		return &api.ErrNotFound{ArtifactID: id}
	}

	delete(cm.Data, id)

	// Clean up version entries
	vcm, verr := s.getConfigMap(ctx, s.versionsName)
	if verr == nil && vcm.Data != nil {
		prefix := id + "-v"
		for key := range vcm.Data {
			if strings.HasPrefix(key, prefix) {
				delete(vcm.Data, key)
			}
		}
		_ = s.updateConfigMap(ctx, vcm)
	}

	return s.updateConfigMap(ctx, cm)
}

func (s *ConfigMapStore) CreateVersion(ctx context.Context, version *api.ArtifactVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getOrCreateConfigMap(ctx, s.versionsName)
	if err != nil {
		return err
	}

	data, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("marshaling version: %w", err)
	}

	key := fmt.Sprintf("%s-v%d", version.ArtifactID, version.Version)
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[key] = string(data)

	return s.updateConfigMap(ctx, cm)
}

func (s *ConfigMapStore) ListVersions(ctx context.Context, artifactID string) ([]api.ArtifactVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.versionsName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	prefix := artifactID + "-v"
	var versions []api.ArtifactVersion
	for key, v := range cm.Data {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		var ver api.ArtifactVersion
		if err := json.Unmarshal([]byte(v), &ver); err != nil {
			continue
		}
		versions = append(versions, ver)
	}

	// Sort by version number ascending
	slices.SortFunc(versions, func(a, b api.ArtifactVersion) int {
		return a.Version - b.Version
	})

	return versions, nil
}

func (s *ConfigMapStore) GetVersion(ctx context.Context, artifactID string, version int) (*api.ArtifactVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx, s.versionsName)
	if err != nil {
		return nil, &api.ErrVersionNotFound{ArtifactID: artifactID, Version: version}
	}

	key := fmt.Sprintf("%s-v%d", artifactID, version)
	data, ok := cm.Data[key]
	if !ok {
		return nil, &api.ErrVersionNotFound{ArtifactID: artifactID, Version: version}
	}

	var ver api.ArtifactVersion
	if err := json.Unmarshal([]byte(data), &ver); err != nil {
		return nil, fmt.Errorf("unmarshaling version: %w", err)
	}
	return &ver, nil
}

func (s *ConfigMapStore) getConfigMap(ctx context.Context, name string) (*corev1.ConfigMap, error) {
	return s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, name, metav1.GetOptions{})
}

func (s *ConfigMapStore) getOrCreateConfigMap(ctx context.Context, name string) (*corev1.ConfigMap, error) {
	cm, err := s.getConfigMap(ctx, name)
	if err == nil {
		return cm, nil
	}

	if !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting configmap: %w", err)
	}

	// Create the ConfigMap
	cm = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "vibed",
				"app.kubernetes.io/component":  "artifact-store",
			},
		},
		Data: make(map[string]string),
	}

	created, err := s.client.CoreV1().ConfigMaps(s.namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("creating configmap: %w", err)
	}
	return created, nil
}

func (s *ConfigMapStore) updateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	_, err := s.client.CoreV1().ConfigMaps(s.namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating configmap: %w", err)
	}
	return nil
}
