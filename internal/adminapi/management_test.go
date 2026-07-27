package adminapi

import (
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/events"
)

func TestNewManagementRouterRegistersAllOperations(t *testing.T) {
	hub, err := events.NewHub(events.Options{
		Capacity: 32, Retention: time.Hour, SubscriberBuffer: 8,
		IDSource: func() (string, error) { return "evt_management", nil },
		Now:      func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	auditService, err := audit.NewService(&fakeAuditStore{})
	if err != nil {
		t.Fatal(err)
	}
	options := ManagementOptions{
		Middleware: MiddlewareOptions{
			PublicOrigin: "https://manager.example", MaxBodyBytes: 1 << 20,
		},
		CookieSecure:  true,
		Auth:          newFakeAuthOperations(),
		Users:         &fakeAdminUserService{},
		Audit:         auditService,
		Overview:      &fakeOverviewReader{},
		Groups:        &fakeGroupOperations{},
		Settings:      &settingsOperationsFake{},
		JoinRequests:  &joinRequestOperationsFake{},
		ScheduledJobs: &fakeScheduledJobOperations{},
		Knowledge:     &knowledgeOperationsFake{},
		Analytics:     &analyticsOperationsFake{},
		Commands:      &fakeCommandOperations{},
		System:        &fakeSystemOperations{},
		Events:        hub,
	}
	router, err := NewManagementRouter(options)
	if err != nil {
		t.Fatal(err)
	}
	operationCount := 0
	for _, group := range router.groups {
		operationCount += len(group.routes)
	}
	if operationCount != 57 {
		t.Fatalf("registered operations=%d, want 57", operationCount)
	}
	for _, route := range []struct{ method, pattern string }{
		{"POST", "/api/admin/v1/auth/login"},
		{"GET", "/api/admin/v1/overview"},
		{"PATCH", "/api/admin/v1/groups/{group_id}/settings"},
		{"POST", "/api/admin/v1/join-requests/{request_id}/decisions"},
		{"POST", "/api/admin/v1/scheduled-jobs/{job_id}/test-send"},
		{"POST", "/api/admin/v1/knowledge/reload"},
		{"GET", "/api/admin/v1/analytics/export"},
		{"POST", "/api/admin/v1/commands/{command_id}/validate"},
		{"POST", "/api/admin/v1/system/napcat/restart"},
		{"GET", "/api/admin/v1/events"},
	} {
		group := router.groups[route.pattern]
		if group == nil {
			t.Fatalf("missing route pattern %s", route.pattern)
		}
		if _, ok := group.routes[route.method]; !ok {
			t.Fatalf("missing route %s %s", route.method, route.pattern)
		}
	}
}

func TestNewManagementRouterRejectsPartialSurface(t *testing.T) {
	if _, err := NewManagementRouter(ManagementOptions{}); err == nil {
		t.Fatal("partial management surface was accepted")
	}
}
