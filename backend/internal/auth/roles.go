package auth

import "slices"

// Permission is a single capability. Handlers check permissions rather than
// roles so that the role table can change without touching authorisation
// checks at the call sites.
type Permission string

const (
	PermDatabaseRead  Permission = "database:read"
	PermDatabaseWrite Permission = "database:write" // create, rename, delete, import
	PermSchemaWrite   Permission = "schema:write"   // tables, columns, indexes
	PermRowsRead      Permission = "rows:read"
	PermRowsWrite     Permission = "rows:write"
	PermSQLRead       Permission = "sql:read"  // run read-only statements
	PermSQLWrite      Permission = "sql:write" // run arbitrary statements
	PermAPIRead       Permission = "api:read"
	PermAPIWrite      Permission = "api:write"
	PermKeysRead      Permission = "keys:read"
	PermKeysWrite     Permission = "keys:write"
	PermBackupRead    Permission = "backup:read"
	PermBackupWrite   Permission = "backup:write" // run, restore, delete
	PermSettingsRead  Permission = "settings:read"
	PermSettingsWrite Permission = "settings:write"
	PermUsersRead     Permission = "users:read"
	PermUsersWrite    Permission = "users:write"
)

// Role names a set of permissions.
type Role string

const (
	// RoleOwner is the bootstrap administrator: full control, including users.
	RoleOwner Role = "owner"
	// RoleAdmin can operate every feature but cannot manage other users.
	RoleAdmin Role = "admin"
	// RoleEditor can read and write data but cannot alter schemas, API
	// definitions, keys or backups.
	RoleEditor Role = "editor"
	// RoleViewer is read-only.
	RoleViewer Role = "viewer"
)

// rolePermissions is the authoritative role table. Multi-user support is a
// future goal, so the roles below are already distinct even though a fresh
// install only creates an owner.
var rolePermissions = map[Role][]Permission{
	RoleOwner: {
		PermDatabaseRead, PermDatabaseWrite, PermSchemaWrite,
		PermRowsRead, PermRowsWrite, PermSQLRead, PermSQLWrite,
		PermAPIRead, PermAPIWrite, PermKeysRead, PermKeysWrite,
		PermBackupRead, PermBackupWrite, PermSettingsRead, PermSettingsWrite,
		PermUsersRead, PermUsersWrite,
	},
	RoleAdmin: {
		PermDatabaseRead, PermDatabaseWrite, PermSchemaWrite,
		PermRowsRead, PermRowsWrite, PermSQLRead, PermSQLWrite,
		PermAPIRead, PermAPIWrite, PermKeysRead, PermKeysWrite,
		PermBackupRead, PermBackupWrite, PermSettingsRead, PermSettingsWrite,
		PermUsersRead,
	},
	RoleEditor: {
		PermDatabaseRead, PermRowsRead, PermRowsWrite, PermSQLRead,
		PermAPIRead, PermBackupRead,
	},
	RoleViewer: {
		PermDatabaseRead, PermRowsRead, PermSQLRead, PermAPIRead, PermBackupRead,
	},
}

// ValidRole reports whether r is a known role.
func ValidRole(r Role) bool {
	_, ok := rolePermissions[r]
	return ok
}

// Roles lists the defined roles, most privileged first.
func Roles() []Role { return []Role{RoleOwner, RoleAdmin, RoleEditor, RoleViewer} }

// Permissions returns the permissions granted by a role.
func (r Role) Permissions() []Permission {
	perms, ok := rolePermissions[r]
	if !ok {
		return nil
	}
	out := make([]Permission, len(perms))
	copy(out, perms)
	return out
}

// Can reports whether the role grants p.
func (r Role) Can(p Permission) bool {
	return slices.Contains(rolePermissions[r], p)
}

// APIScopes are the capabilities an API key may be granted. They are
// intentionally separate from user permissions: keys address the generated
// data API, not the admin dashboard.
const (
	ScopeRead  = "read"  // GET endpoints
	ScopeWrite = "write" // POST, PUT, PATCH, DELETE endpoints
)

// ValidScope reports whether s is a known API key scope.
func ValidScope(s string) bool { return s == ScopeRead || s == ScopeWrite }

// ScopeForMethod maps an HTTP method onto the scope it requires.
func ScopeForMethod(method string) string {
	if method == "GET" || method == "HEAD" {
		return ScopeRead
	}
	return ScopeWrite
}
