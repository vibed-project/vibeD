package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/maxkorbacher/vibed/pkg/api"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ConfigMapStore persists artifact metadata in a Kubernetes ConfigMap.
// Each artifact is stored as a JSON entry keyed by its ID.
type ConfigMapStore struct {
	client    kubernetes.Interface
	name      string
	namespace string
	mu        sync.Mutex
}

// NewConfigMapStore creates a ConfigMap-backed artifact store.
func NewConfigMapStore(client kubernetes.Interface, name, namespace string) *ConfigMapStore {
	return &ConfigMapStore{
		client:    client,
		name:      name,
		namespace: namespace,
	}
}

func (s *ConfigMapStore) Create(ctx context.Context, artifact *api.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getOrCreateConfigMap(ctx)
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

	cm, err := s.getConfigMap(ctx)
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

	cm, err := s.getConfigMap(ctx)
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

func (s *ConfigMapStore) List(ctx context.Context, statusFilter string, ownerID string) ([]api.ArtifactSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx)
	if err != nil {
		// If ConfigMap doesn't exist yet, return empty list
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	var summaries []api.ArtifactSummary
	for _, v := range cm.Data {
		var artifact api.Artifact
		if err := json.Unmarshal([]byte(v), &artifact); err != nil {
			continue
		}
		if statusFilter != "" && statusFilter != "all" && string(artifact.Status) != statusFilter {
			continue
		}
		if ownerID != "" && artifact.OwnerID != ownerID {
			continue
		}
		summaries = append(summaries, artifact.ToSummary())
	}
	return summaries, nil
}

func (s *ConfigMapStore) Update(ctx context.Context, artifact *api.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cm, err := s.getConfigMap(ctx)
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

	cm, err := s.getConfigMap(ctx)
	if err != nil {
		return err
	}

	if _, ok := cm.Data[id]; !ok {
		return &api.ErrNotFound{ArtifactID: id}
	}

	delete(cm.Data, id)
	return s.updateConfigMap(ctx, cm)
}

func (s *ConfigMapStore) getConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	return s.client.CoreV1().ConfigMaps(s.namespace).Get(ctx, s.name, metav1.GetOptions{})
}

func (s *ConfigMapStore) getOrCreateConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	cm, err := s.getConfigMap(ctx)
	if err == nil {
		return cm, nil
	}

	if !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting configmap: %w", err)
	}

	// Create the ConfigMap
	cm = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      s.name,
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
