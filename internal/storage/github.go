package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

// GitHubStorage stores artifact source code and manifests in a GitHub repository.
// Each artifact gets its own folder under artifacts/{artifactID}/.
type GitHubStorage struct {
	client   *github.Client
	owner    string
	repo     string
	branch   string
	localDir string // Local cache directory for build operations
}

// NewGitHubStorage creates a GitHubStorage backend.
func NewGitHubStorage(owner, repo, branch, token, localCacheDir string) (*GitHubStorage, error) {
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("github storage requires owner and repo")
	}

	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN is required for GitHub storage")
	}

	if err := os.MkdirAll(localCacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating local cache dir: %w", err)
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	client := github.NewClient(tc)

	return &GitHubStorage{
		client:   client,
		owner:    owner,
		repo:     repo,
		branch:   branch,
		localDir: localCacheDir,
	}, nil
}

func (s *GitHubStorage) StoreSource(ctx context.Context, artifactID string, files map[string]string) (*StorageRef, error) {
	// 1. Write files locally for the deploy path to read back
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

	// 2. Commit files to GitHub using the Git Trees API for atomic multi-file commits
	var treeEntries []*github.TreeEntry
	for relPath, content := range files {
		ghPath := fmt.Sprintf("artifacts/%s/src/%s", artifactID, relPath)
		fileContent := content // copy for pointer
		treeEntries = append(treeEntries, &github.TreeEntry{
			Path:    github.Ptr(ghPath),
			Mode:    github.Ptr("100644"),
			Type:    github.Ptr("blob"),
			Content: github.Ptr(fileContent),
		})
	}

	if err := s.commitTree(ctx, treeEntries, fmt.Sprintf("vibed: store source for %s", artifactID)); err != nil {
		// Log but don't fail — local files are available for the build
		fmt.Fprintf(os.Stderr, "WARNING: failed to commit to GitHub: %v\n", err)
	}

	return &StorageRef{
		Backend:   "github",
		LocalPath: localSrcDir,
		RemoteRef: fmt.Sprintf("%s/%s/tree/%s/artifacts/%s", s.owner, s.repo, s.branch, artifactID),
	}, nil
}

func (s *GitHubStorage) StoreManifest(ctx context.Context, artifactID string, manifests map[string][]byte) error {
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

	// Commit to GitHub
	var treeEntries []*github.TreeEntry
	for filename, content := range manifests {
		ghPath := fmt.Sprintf("artifacts/%s/manifests/%s", artifactID, filename)
		contentStr := string(content)
		treeEntries = append(treeEntries, &github.TreeEntry{
			Path:    github.Ptr(ghPath),
			Mode:    github.Ptr("100644"),
			Type:    github.Ptr("blob"),
			Content: github.Ptr(contentStr),
		})
	}

	return s.commitTree(ctx, treeEntries, fmt.Sprintf("vibed: store manifests for %s", artifactID))
}

func (s *GitHubStorage) GetSourcePath(_ context.Context, artifactID string) (string, error) {
	srcDir := filepath.Join(s.localDir, artifactID, "src")
	if _, err := os.Stat(srcDir); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("source not found for artifact %q (local cache miss)", artifactID)
		}
		return "", err
	}
	return srcDir, nil
}

func (s *GitHubStorage) Delete(ctx context.Context, artifactID string) error {
	// Delete local cache
	os.RemoveAll(filepath.Join(s.localDir, artifactID))

	// Delete from GitHub by listing and removing files
	_, dirContent, _, err := s.client.Repositories.GetContents(
		ctx, s.owner, s.repo,
		fmt.Sprintf("artifacts/%s", artifactID),
		&github.RepositoryContentGetOptions{Ref: s.branch},
	)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil // Already deleted
		}
		return fmt.Errorf("listing GitHub contents: %w", err)
	}

	for _, entry := range dirContent {
		_, _, err := s.client.Repositories.DeleteFile(ctx, s.owner, s.repo, entry.GetPath(),
			&github.RepositoryContentFileOptions{
				Message: github.Ptr(fmt.Sprintf("vibed: delete %s", artifactID)),
				SHA:     entry.SHA,
				Branch:  github.Ptr(s.branch),
			})
		if err != nil {
			return fmt.Errorf("deleting file %s: %w", entry.GetPath(), err)
		}
	}

	return nil
}

// commitTree creates a new commit with the given tree entries.
func (s *GitHubStorage) commitTree(ctx context.Context, entries []*github.TreeEntry, message string) error {
	// Get the reference for the branch
	ref, _, err := s.client.Git.GetRef(ctx, s.owner, s.repo, "refs/heads/"+s.branch)
	if err != nil {
		return fmt.Errorf("getting ref: %w", err)
	}

	// Get the latest commit
	parentSHA := ref.Object.GetSHA()
	parentCommit, _, err := s.client.Git.GetCommit(ctx, s.owner, s.repo, parentSHA)
	if err != nil {
		return fmt.Errorf("getting parent commit: %w", err)
	}

	// Create a new tree based on the parent tree
	tree, _, err := s.client.Git.CreateTree(ctx, s.owner, s.repo, parentCommit.Tree.GetSHA(), entries)
	if err != nil {
		return fmt.Errorf("creating tree: %w", err)
	}

	// Create the commit
	newCommit, _, err := s.client.Git.CreateCommit(ctx, s.owner, s.repo, &github.Commit{
		Message: github.Ptr(message),
		Tree:    tree,
		Parents: []*github.Commit{{SHA: github.Ptr(parentSHA)}},
	}, nil)
	if err != nil {
		return fmt.Errorf("creating commit: %w", err)
	}

	// Update the reference
	ref.Object.SHA = newCommit.SHA
	_, _, err = s.client.Git.UpdateRef(ctx, s.owner, s.repo, ref, false)
	if err != nil {
		return fmt.Errorf("updating ref: %w", err)
	}

	return nil
}
