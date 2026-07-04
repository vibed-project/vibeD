package v1alpha1

// Label keys vibeD stamps on VibedApp objects for owner/department filtering
// (quota counting, list scoping). The authoritative owner is always
// spec.owner; these labels mirror it (sanitized) only so they're selectable.
const (
	LabelOwner      = "vibed.dev/owner"
	LabelDepartment = "vibed.dev/department"
	// LabelTenant identifies the tenant an app belongs to. It is stamped only in
	// multi-tenant mode (a non-empty tenant ID); the single-tenant default omits
	// it, so existing single-tenant apps are unchanged.
	LabelTenant = "vibed.dev/tenant"
)

// SanitizeLabel makes an arbitrary identity safe as a Kubernetes label value
// (≤63 chars, alnum/-/_/. only). Quota counting relies on the deploy path and
// the enforcer producing the SAME sanitized value, so both call this.
func SanitizeLabel(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return string(out)
}
