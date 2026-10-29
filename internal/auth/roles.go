package auth

import "net/http"

// Role is a user's permission level. Each role includes the rights of the
// roles before it: viewer < operator < admin.
type Role string

const (
	// RoleViewer can read everything but change nothing.
	RoleViewer Role = "viewer"
	// RoleOperator can also run actions, acknowledge tasks and use AI features.
	RoleOperator Role = "operator"
	// RoleAdmin can also add, change and remove instances.
	RoleAdmin Role = "admin"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ParseRole validates a role name.
func ParseRole(s string) (Role, bool) {
	r := Role(s)
	_, ok := roleRank[r]
	return r, ok
}

// Allows reports whether r has at least the rights of min. Unknown roles have none.
func (r Role) Allows(min Role) bool {
	return roleRank[r] > 0 && roleRank[r] >= roleRank[min]
}

// RequireRole is Require plus a minimum role. It answers 403 when the
// authenticated user's role is too low.
func (m *Manager) RequireRole(min Role, next http.Handler) http.Handler {
	return m.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Role(SessionFrom(r.Context()).Role).Allows(min) {
			http.Error(w, `{"error":"forbidden: requires role `+string(min)+`"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
