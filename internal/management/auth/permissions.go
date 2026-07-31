package auth

import "errors"

var (
	ErrInvalidRole       = errors.New("invalid admin role")
	ErrInvalidPermission = errors.New("invalid admin permission")
)

var allPermissions = []Permission{
	PermissionOverviewRead,
	PermissionGroupsRead,
	PermissionGroupsSync,
	PermissionSettingsRead,
	PermissionSettingsWrite,
	PermissionJoinRequestsRead,
	PermissionJoinRequestsDecide,
	PermissionJoinPoliciesWrite,
	PermissionCommandsRead,
	PermissionCommandsWrite,
	PermissionScheduledJobsRead,
	PermissionScheduledJobsWrite,
	PermissionKnowledgeRead,
	PermissionKnowledgeReload,
	PermissionAnalyticsRead,
	PermissionAnalyticsExport,
	PermissionAuditRead,
	PermissionUsersManage,
	PermissionSessionsManage,
	PermissionSystemRead,
	PermissionConfigWrite,
	PermissionNapCatRestart,
	PermissionBotRestart,
	PermissionEventsRead,
}

var maintainerPermissions = []Permission{
	PermissionOverviewRead,
	PermissionGroupsRead,
	PermissionGroupsSync,
	PermissionSettingsRead,
	PermissionSettingsWrite,
	PermissionJoinRequestsRead,
	PermissionJoinRequestsDecide,
	PermissionCommandsRead,
	PermissionCommandsWrite,
	PermissionScheduledJobsRead,
	PermissionScheduledJobsWrite,
	PermissionKnowledgeRead,
	PermissionKnowledgeReload,
	PermissionAnalyticsRead,
	PermissionAnalyticsExport,
	PermissionAuditRead,
	PermissionSystemRead,
	PermissionEventsRead,
}

var observerPermissions = []Permission{
	PermissionOverviewRead,
	PermissionGroupsRead,
	PermissionSettingsRead,
	PermissionJoinRequestsRead,
	PermissionCommandsRead,
	PermissionScheduledJobsRead,
	PermissionKnowledgeRead,
	PermissionAnalyticsRead,
	PermissionAnalyticsExport,
	PermissionAuditRead,
	PermissionSystemRead,
	PermissionEventsRead,
}

func ParseRole(value string) (Role, error) {
	role := Role(value)
	switch role {
	case RoleSuperAdmin, RoleMaintainer, RoleObserver:
		return role, nil
	default:
		return "", ErrInvalidRole
	}
}

func ParsePermission(value string) (Permission, error) {
	permission := Permission(value)
	for _, candidate := range allPermissions {
		if permission == candidate {
			return permission, nil
		}
	}
	return "", ErrInvalidPermission
}

func AllPermissions() []Permission {
	return append([]Permission(nil), allPermissions...)
}

func PermissionsFor(role Role) []Permission {
	switch role {
	case RoleSuperAdmin:
		return append([]Permission(nil), allPermissions...)
	case RoleMaintainer:
		return append([]Permission(nil), maintainerPermissions...)
	case RoleObserver:
		return append([]Permission(nil), observerPermissions...)
	default:
		return nil
	}
}

func Allowed(role Role, permission Permission) bool {
	for _, candidate := range permissionsForRead(role) {
		if permission == candidate {
			return true
		}
	}
	return false
}

func permissionsForRead(role Role) []Permission {
	switch role {
	case RoleSuperAdmin:
		return allPermissions
	case RoleMaintainer:
		return maintainerPermissions
	case RoleObserver:
		return observerPermissions
	default:
		return nil
	}
}
