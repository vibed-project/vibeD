package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gitlab "gitlab.com/gitlab-org/api/client-go"
)

// GitLabStorage stores artifact source code and manifests in a GitLab repository.
// Each artifact gets its own folder under artifacts/{artifactID}/.
// It uses the GitLab Commits API for atomic multi-file operations.
type GitLabStorage struct {
	client    *gitlab.Client
	projectID int
	branch    string
	localDir  string // Local cache directory for build operations
}

// NewGitLabStorage creates a GitLabStorage backend.
func NewGitLabStorage(url string, projectID int, branch, token, localCacheDir string) (*GitLabStorage, error) {
	if projectID == 0 {
		return nil, fmt.Errorf("gitlab storage requires a projectID")
	}

	if token == "" {
		token = os.Getenv("GITLAB_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("GITLAB_TOKEN is required for GitLab storage")
	}

	if branch == "" {
		branch = "main"
	}

	if url == "" {
		url = "https://gitlab.com"
	}

	if err := os.MkdirAll(localCacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating local cache dir: %w", err)
	}

	client, err := gitlab.NewClient(token, gitlab.WithBaseURL(url+"/api/v4"))
	if err != nil {
		return nil, fmt.Errorf("creating GitLab client: %w", err)
	}

	return &GitLabStorage{
		client:    client,
		projectID: projectID,
		branch:    branch,
		localDir:  localCacheDir,
	}, nil
}

func (s *GitLabStorage) StoreSource(ctx context.Context, artifactID string, files map[string]string) (*StorageRef, error) {
	// 1. Write files locally for buildpacks to use
	localSrcDir := filepath.Join(s.localDir, artifactID, "src")
	if err := os.MkdirAll(localSrcDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating local src dir: %w", err)
	}
	for relPath, content := range files {
		fullPath := filepath.Join(localSrcDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			return nil, err
		}
	}

	// 2. Commit files to GitLab using the Commits API for atomic multi-file commits
	var actions []*gitlab.CommitActionOptions
	for relPath, content := range files {
		glPath := fmt.Sprintf("artifacts/%s/src/%s", artifactID, relPath)
		fileContent := content // copy for pointer
		action := gitlab.FileCreate
		actions = append(actions, &gitlab.CommitActionOptions{
			Action:   &action,
			FilePath: &glPath,
			Content:  &fileContent,
		})
	}

	commitMsg := fmt.Sprintf("vibed: store source for %s", artifactID)
	_, _, err := s.client.Commits.CreateCommit(s.projectID, &gitlab.CreateCommitOptions{
		Branch:        &s.branch,
		CommitMessage: &commitMsg,
		Actions:       actions,
	}, gitlab.WithContext(ctx))
	if err != nil {
		// Log but don't fail — local files are available for the build
		fmt.Fprintf(os.Stderr, "WARNING: failed to commit to GitLab: %v\n", err)
	}

	return &StorageRef{
		Backend:   "gitlab",
		LocalPath: localSrcDir,
		RemoteRef: fmt.Sprintf("project/%d/tree/%s/artifacts/%s", s.projectID, s.branch, artifactID),
	}, nil
}

func (s *GitLabStorage) StoreManifest(ctx context.Context, artifactID string, manifests map[string][]byte) error {
	// Write locally
	localManifestDir := filepath.Join(s.localDir, artifactID, "manifests")
	if err := os.MkdirAll(localManifestDir, 0o755); err != nil {
		return fmt.Errorf("creating local manifest dir: %w", err)
	}
	for filename, content := range manifests {
		if err := os.WriteFile(filepath.Join(localManifestDir, filename), content, 0o644); err != nil {
			return err
		}
	}

	// Commit to GitLab
	var actions []*gitlab.CommitActionOptions
	for filename, content := range manifests {
		glPath := fmt.Sprintf("artifacts/%s/manifests/%s", artifactID, filename)
		contentStr := string(content)
		action := gitlab.FileCreate
		actions = append(actions, &gitlab.CommitActionOptions{
			Action:   &action,
			FilePath: &glPath,
			Content:  &contentStr,
		})
	}

	commitMsg := fmt.Sprintf("vibed: store manifests for %s", artifactID)
	_, _, err := s.client.Commits.CreateCommit(s.projectID, &gitlab.CreateCommitOptions{
		Branch:        &s.branch,
		CommitMessage: &commitMsg,
		Actions:       actions,
	}, gitlab.WithContext(ctx))
	return err
}

func (s *GitLabStorage) GetSourcePath(_ context.Context, artifactID string) (string, error) {
	srcDir := filepath.Join(s.localDir, artifactID, "src")
	if _, err := os.Stat(srcDir); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("source not found for artifact %q (local cache miss)", artifactID)
		}
		return "", err
	}
	return srcDir, nil
}

func (s *GitLabStorage) Delete(ctx context.Context, artifactID string) error {
	// Delete local cache
	os.RemoveAll(filepath.Join(s.localDir, artifactID))

	// List files from GitLab to build delete actions
	path := fmt.Sprintf("artifacts/%s", artifactID)
	recursive := true
	tree, _, err := s.client.Repositories.ListTree(s.projectID, &gitlab.ListTreeOptions{
		Path:      &path,
		Recursive: &recursive,
		Ref:       &s.branch,
	}, gitlab.WithContext(ctx))
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "Tree Not Found") {
			return nil // Already deleted
		}
		return fmt.Errorf("listing GitLab tree: %w", err)
	}

	if len(tree) == 0 {
		return nil
	}

	// Build delete actions for all blob entries
	var actions []*gitlab.CommitActionOptions
	for _, entry := range tree {
		if entry.Type == "blob" {
			action := gitlab.FileDelete
			entryPath := entry.Path
			actions = append(actions, &gitlab.CommitActionOptions{
				Action:   &action,
				FilePath: &entryPath,
			})
		}
	}

	if len(actions) == 0 {
		return nil
	}

	commitMsg := fmt.Sprintf("vibed: delete %s", artifactID)
	_, _, err = s.client.Commits.CreateCommit(s.projectID, &gitlab.CreateCommitOptions{
		Branch:        &s.branch,
		CommitMessage: &commitMsg,
		Actions:       actions,
	}, gitlab.WithContext(ctx))
	return err
}
