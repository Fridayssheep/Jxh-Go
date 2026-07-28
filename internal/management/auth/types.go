package auth

import "time"

type Role string

const (
	RoleSuperAdmin Role = "super_admin"
	RoleMaintainer Role = "maintainer"
	RoleObserver   Role = "observer"
)

type Permission string

const (
	PermissionOverviewRead       Permission = "overview:read"
	PermissionGroupsRead         Permission = "groups:read"
	PermissionGroupsSync         Permission = "groups:sync"
	PermissionSettingsRead       Permission = "settings:read"
	PermissionSettingsWrite      Permission = "settings:write"
	PermissionJoinRequestsRead   Permission = "join_requests:read"
	PermissionJoinRequestsDecide Permission = "join_requests:decide"
	PermissionJoinPoliciesWrite  Permission = "join_policies:write"
	PermissionCommandsRead       Permission = "commands:read"
	PermissionCommandsWrite      Permission = "commands:write"
	PermissionScheduledJobsRead  Permission = "scheduled_jobs:read"
	PermissionScheduledJobsWrite Permission = "scheduled_jobs:write"
	PermissionKnowledgeRead      Permission = "knowledge:read"
	PermissionKnowledgeReload    Permission = "knowledge:reload"
	PermissionAnalyticsRead      Permission = "analytics:read"
	PermissionAnalyticsExport    Permission = "analytics:export"
	PermissionAuditRead          Permission = "audit:read"
	PermissionUsersManage        Permission = "users:manage"
	PermissionSessionsManage     Permission = "sessions:manage"
	PermissionSystemRead         Permission = "system:read"
	PermissionConfigWrite        Permission = "config:write"
	PermissionNapCatRestart      Permission = "napcat:restart"
	PermissionEventsRead         Permission = "events:read"
)

type SessionStatus string

const (
	SessionStatusActive  SessionStatus = "active"
	SessionStatusExpired SessionStatus = "expired"
	SessionStatusRevoked SessionStatus = "revoked"
)

type User struct {
	ID          string
	Username    string
	DisplayName string
	Role        Role
	QQUserID    *string
	Enabled     bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Version     uint64
}

type Session struct {
	ID                string
	UserID            string
	Status            SessionStatus
	Current           bool
	IPAddress         string
	UserAgent         string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	ExpiresAt         time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
}

type Principal struct {
	UserID    string
	SessionID string
	Role      Role
}

func (p Principal) Has(permission Permission) bool {
	return Allowed(p.Role, permission)
}

type Field[T any] struct {
	Set   bool
	Value T
}
