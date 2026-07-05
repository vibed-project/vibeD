package auth

import (
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

func TestParseOIDCLeeway(t *testing.T) {
	if d, err := parseOIDCLeeway(""); err != nil || d != 0 {
		t.Fatalf("empty: got %v, %v; want 0, nil", d, err)
	}
	if d, err := parseOIDCLeeway("2m"); err != nil || d != 2*time.Minute {
		t.Fatalf("2m: got %v, %v", d, err)
	}
	if _, err := parseOIDCLeeway("nonsense"); err == nil {
		t.Error("malformed leeway should error")
	}
	if _, err := parseOIDCLeeway("-30s"); err == nil {
		t.Error("negative leeway should error")
	}
}

func TestCheckAuthorizedParty(t *testing.T) {
	// azp present and matching → ok.
	if err := checkAuthorizedParty(map[string]any{"azp": "vibed"}, "vibed"); err != nil {
		t.Errorf("matching azp: %v", err)
	}
	// azp present but wrong → rejected.
	if err := checkAuthorizedParty(map[string]any{"azp": "other"}, "vibed"); err == nil {
		t.Error("mismatched azp must be rejected")
	}
	// azp absent, single audience → ok.
	if err := checkAuthorizedParty(map[string]any{"aud": "vibed"}, "vibed"); err != nil {
		t.Errorf("absent azp single aud: %v", err)
	}
	// azp absent, multi audience → rejected (azp required).
	if err := checkAuthorizedParty(map[string]any{"aud": []any{"vibed", "other"}}, "vibed"); err == nil {
		t.Error("multi-audience token without azp must be rejected")
	}
}

func TestCheckOIDCTimes(t *testing.T) {
	now := time.Now()
	// Leeway 0 → no-op even for an expired token (go-oidc handled expiry).
	expired := &oidc.IDToken{Expiry: now.Add(-time.Hour)}
	if err := checkOIDCTimes(expired, nil, 0, now); err != nil {
		t.Errorf("leeway=0 must be a no-op: %v", err)
	}
	// Expired within leeway → accepted.
	if err := checkOIDCTimes(&oidc.IDToken{Expiry: now.Add(-30 * time.Second)}, nil, 2*time.Minute, now); err != nil {
		t.Errorf("expired within leeway should pass: %v", err)
	}
	// Expired beyond leeway → rejected.
	if err := checkOIDCTimes(&oidc.IDToken{Expiry: now.Add(-5 * time.Minute)}, nil, 2*time.Minute, now); err == nil {
		t.Error("expired beyond leeway must be rejected")
	}
	// Issued slightly in the future within leeway → accepted; beyond → rejected.
	if err := checkOIDCTimes(&oidc.IDToken{Expiry: now.Add(time.Hour), IssuedAt: now.Add(30 * time.Second)}, nil, 2*time.Minute, now); err != nil {
		t.Errorf("iat within leeway should pass: %v", err)
	}
	if err := checkOIDCTimes(&oidc.IDToken{Expiry: now.Add(time.Hour), IssuedAt: now.Add(5 * time.Minute)}, nil, 2*time.Minute, now); err == nil {
		t.Error("iat beyond leeway must be rejected")
	}
	// nbf in the future beyond leeway → rejected.
	claims := map[string]any{"nbf": float64(now.Add(5 * time.Minute).Unix())}
	if err := checkOIDCTimes(&oidc.IDToken{Expiry: now.Add(time.Hour)}, claims, 2*time.Minute, now); err == nil {
		t.Error("nbf beyond leeway must be rejected")
	}
}
