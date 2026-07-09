package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/vibed-project/vibeD/pkg/api"
	vibedv1 "github.com/vibed-project/vibeD/pkg/vibedapi/v1alpha1"
)

// CreateShareLink mints a public, optionally password-protected link to an app
// the owner owns. The token is the credential; a link resolves to the app's URL.
func (s *Service) CreateShareLink(ctx context.Context, owner, appID, password string, expiresIn time.Duration) (*api.ShareLink, error) {
	if s.ShareLinks == nil {
		return nil, fmt.Errorf("share links require the sqlite store backend")
	}
	if _, err := s.Get(ctx, owner, appID); err != nil { // ownership-checked
		return nil, err
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	var passwordHash string
	if password != "" {
		h, herr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if herr != nil {
			return nil, fmt.Errorf("hash password: %w", herr)
		}
		passwordHash = string(h)
	}

	now := time.Now()
	link := &api.ShareLink{
		Token:       token,
		ArtifactID:  appID,
		CreatedBy:   owner,
		HasPassword: password != "",
		CreatedAt:   now,
	}
	if expiresIn > 0 {
		exp := now.Add(expiresIn)
		link.ExpiresAt = &exp
	}
	if s.BaseURL != "" {
		link.URL = s.BaseURL + "/share/" + token
	}
	if err := s.ShareLinks.CreateShareLink(ctx, link, passwordHash); err != nil {
		return nil, err
	}
	return link, nil
}

// ListShareLinks returns the app's share links if owner owns it.
func (s *Service) ListShareLinks(ctx context.Context, owner, appID string) ([]api.ShareLink, error) {
	if s.ShareLinks == nil {
		return nil, fmt.Errorf("share links require the sqlite store backend")
	}
	if _, err := s.Get(ctx, owner, appID); err != nil {
		return nil, err
	}
	return s.ShareLinks.ListShareLinks(ctx, appID)
}

// RevokeShareLink revokes a token if owner owns the app it points at.
func (s *Service) RevokeShareLink(ctx context.Context, owner, token string) error {
	if s.ShareLinks == nil {
		return fmt.Errorf("share links require the sqlite store backend")
	}
	link, _, err := s.ShareLinks.GetShareLink(ctx, token)
	if err != nil {
		return err
	}
	if _, err := s.Get(ctx, owner, link.ArtifactID); err != nil {
		return err
	}
	return s.ShareLinks.RevokeShareLink(ctx, token)
}

// ResolveShareLink validates a public token (and optional password) and returns
// the target app. Unauthenticated — the token is the credential. Revoked,
// expired, and unknown tokens all return ErrShareLinkNotFound (no leakage); a
// missing/wrong password returns ErrPasswordRequired.
func (s *Service) ResolveShareLink(ctx context.Context, token, password string) (*vibedv1.VibedApp, error) {
	if s.ShareLinks == nil {
		return nil, fmt.Errorf("share links require the sqlite store backend")
	}
	s.defaults()
	link, passwordHash, err := s.ShareLinks.GetShareLink(ctx, token)
	if err != nil {
		return nil, err
	}
	if link.Revoked || (link.ExpiresAt != nil && time.Now().After(*link.ExpiresAt)) {
		return nil, &api.ErrShareLinkNotFound{Token: token}
	}
	if passwordHash != "" {
		if password == "" || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
			return nil, &api.ErrPasswordRequired{}
		}
	}

	app := &vibedv1.VibedApp{}
	if gerr := s.Client.Get(ctx, types.NamespacedName{Name: link.ArtifactID, Namespace: s.Namespace}, app); gerr != nil {
		return nil, &api.ErrShareLinkNotFound{Token: token}
	}
	return app, nil
}
