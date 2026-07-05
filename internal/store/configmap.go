package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/vibed-project/vibeD/pkg/api"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// maxConfigMapDataBytes is a conservative pre-write ceiling on the total size of
// a ConfigMap's Data. etcd caps an object at ~1 MiB (1_048_576 bytes); we guard
// well below it (900 KiB) to leave headroom for the object's metadata and the
// JSON/base64 overhead so a write that would blow the etcd limit is rejected
// with a clear error instead of failing opaquely at the API server (#71).
const maxConfigMapDataBytes = 900 * 1024

// ConfigMapStore persists artifact metadata in a Kubernetes ConfigMap.
// Each artifact is stored as a JSON entry keyed by its ID.
// Versions are stored in a separate ConfigMap named "{name}-versions".
//
// Concurrency (#72): the store holds no process-wide lock across API I/O.
// Reads go straight to the API server. Writes use optimistic concurrency —
// read-modify-write with the object's ResourceVersion and retry-on-conflict —
// so concurrent writers don't clobber each other and no request is serialized
// behind another's network round-trip.
type ConfigMapStore struct {
	client       kubernetes.Interface
	name         string
	versionsName string // e.g. "vibed-artifacts-versions"
	namespace    string
}

func init() {
	Register("configmap", func(d Deps) (ArtifactStore, error) {
		return NewConfigMapStore(d.K8sClient, d.ConfigMapName, d.ConfigMapNamespace), nil
	})
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
	data, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("marshaling artifact: %w", err)
	}
	return s.mutate(ctx, s.name, true, func(cm *corev1.ConfigMap) error {
		// Check for name collision (and re-check on every retry against the
		// latest data, so a concurrent create of the same name still loses).
		for _, v := range cm.Data {
			var existing api.Artifact
			if json.Unmarshal([]byte(v), &existing) == nil && existing.Name == artifact.Name {
				return &api.ErrAlreadyExists{Name: artifact.Name}
			}
		}
		cm.Data[artifact.ID] = string(data)
		return nil
	})
}

func (s *ConfigMapStore) Get(ctx context.Context, id string) (*api.Artifact, error) {

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
	data, err := json.Marshal(artifact)
	if err != nil {
		return fmt.Errorf("marshaling artifact: %w", err)
	}
	return s.mutate(ctx, s.name, false, func(cm *corev1.ConfigMap) error {
		if _, ok := cm.Data[artifact.ID]; !ok {
			return &api.ErrNotFound{ArtifactID: artifact.ID}
		}
		cm.Data[artifact.ID] = string(data)
		return nil
	})
}

func (s *ConfigMapStore) Delete(ctx context.Context, id string) error {
	if err := s.mutate(ctx, s.name, false, func(cm *corev1.ConfigMap) error {
		if _, ok := cm.Data[id]; !ok {
			return &api.ErrNotFound{ArtifactID: id}
		}
		delete(cm.Data, id)
		return nil
	}); err != nil {
		return err
	}

	// Best-effort cleanup of the artifact's version entries in the separate
	// versions ConfigMap. A missing versions CM is fine.
	prefix := id + "-v"
	_ = s.mutate(ctx, s.versionsName, false, func(cm *corev1.ConfigMap) error {
		for key := range cm.Data {
			if strings.HasPrefix(key, prefix) {
				delete(cm.Data, key)
			}
		}
		return nil
	})
	return nil
}

func (s *ConfigMapStore) CreateVersion(ctx context.Context, version *api.ArtifactVersion) error {
	data, err := json.Marshal(version)
	if err != nil {
		return fmt.Errorf("marshaling version: %w", err)
	}
	key := fmt.Sprintf("%s-v%d", version.ArtifactID, version.Version)
	return s.mutate(ctx, s.versionsName, true, func(cm *corev1.ConfigMap) error {
		cm.Data[key] = string(data)
		return nil
	})
}

func (s *ConfigMapStore) ListVersions(ctx context.Context, artifactID string) ([]api.ArtifactVersion, error) {

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
		// Return the raw API error (unwrapped) so callers can classify it — a
		// concurrent creator triggers AlreadyExists, which mutate retries.
		return nil, err
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

// configMapDataBytes returns the total byte size of a ConfigMap's Data map
// (keys + values), the quantity that counts against the etcd object ceiling.
func configMapDataBytes(data map[string]string) int {
	n := 0
	for k, v := range data {
		n += len(k) + len(v)
	}
	return n
}

// mutate performs an optimistic-concurrency read-modify-write on the named
// ConfigMap: it fetches the current object (fresh ResourceVersion), applies
// apply, enforces the size guard, and Updates — retrying the whole cycle on a
// conflict (another writer won the race). The ConfigMap is created first when
// create is true and it doesn't exist yet. No lock is held across the API I/O,
// so writers don't serialize behind one another (#72) and a stale write can't
// clobber a concurrent one (#71 size guard + ResourceVersion).
//
// apply mutates cm.Data in place; it may return an error to abort (e.g. a
// not-found or already-exists precondition) without retrying.
func (s *ConfigMapStore) mutate(ctx context.Context, name string, create bool, apply func(cm *corev1.ConfigMap) error) error {
	var applyErr error
	// Retry on Conflict (a concurrent Update won the ResourceVersion race) AND
	// on AlreadyExists (two writers raced to first-create the ConfigMap via the
	// getOrCreate path — the loser re-reads the now-existing object next pass).
	shouldRetry := func(err error) bool {
		return k8serrors.IsConflict(err) || k8serrors.IsAlreadyExists(err)
	}
	retryErr := retry.OnError(retry.DefaultRetry, shouldRetry, func() error {
		var (
			cm  *corev1.ConfigMap
			err error
		)
		if create {
			cm, err = s.getOrCreateConfigMap(ctx, name)
		} else {
			cm, err = s.getConfigMap(ctx, name)
		}
		if err != nil {
			return err
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}

		if applyErr = apply(cm); applyErr != nil {
			return nil // precondition failure — stop retrying, surface applyErr
		}

		// Pre-write size guard: reject before the API server would (#71).
		if size := configMapDataBytes(cm.Data); size > maxConfigMapDataBytes {
			applyErr = fmt.Errorf("configmap %q would exceed the %d-byte data limit (%d bytes); "+
				"the configmap store is not suited to this many/large artifacts — use the sqlite store backend",
				name, maxConfigMapDataBytes, size)
			return nil
		}

		_, err = s.client.CoreV1().ConfigMaps(s.namespace).Update(ctx, cm, metav1.UpdateOptions{})
		return err // a Conflict here triggers a retry with a fresh read
	})
	if applyErr != nil {
		return applyErr
	}
	if retryErr != nil {
		return fmt.Errorf("updating configmap %q: %w", name, retryErr)
	}
	return nil
}
