package adminapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/events"
	"gopkg.in/yaml.v3"
)

func TestNewManagementRouterRegistersAllOperations(t *testing.T) {
	router := newCompleteManagementRouter(t)
	operationCount := 0
	for _, group := range router.groups {
		operationCount += len(group.routes)
	}
	if operationCount != 61 {
		t.Fatalf("registered operations=%d, want 61", operationCount)
	}
	for _, route := range []struct{ method, pattern string }{
		{"POST", "/api/admin/v1/auth/login"},
		{"GET", "/api/admin/v1/overview"},
		{"PATCH", "/api/admin/v1/groups/{group_id}/settings"},
		{"POST", "/api/admin/v1/join-requests/{request_id}/decisions"},
		{"GET", "/api/admin/v1/join-request-rules/student-id"},
		{"PATCH", "/api/admin/v1/join-request-rules/student-id"},
		{"POST", "/api/admin/v1/scheduled-jobs/{job_id}/test-send"},
		{"POST", "/api/admin/v1/knowledge/reload"},
		{"GET", "/api/admin/v1/analytics/export"},
		{"POST", "/api/admin/v1/commands/{command_id}/validate"},
		{"GET", "/api/admin/v1/system/configuration"},
		{"PATCH", "/api/admin/v1/system/configuration"},
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

func TestImplementedOpenAPIOperationsMatchManagementRoutes(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "..", "docs", "api", "jxh-manager-openapi.yaml")
	document, err := os.ReadFile(path)
	if errorsIsNotExist(err) {
		t.Skip("workspace OpenAPI document is not present in a standalone bot checkout")
	}
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]openAPIPath `yaml:"paths"`
	}
	if err := yaml.Unmarshal(document, &spec); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]string)
	operationIDs := make(map[string]string)
	for path, item := range spec.Paths {
		for method, operation := range item.operations() {
			if operation == nil {
				continue
			}
			if operation.Status != "implemented" {
				t.Fatalf("OpenAPI operation %s %s has x-status=%q", method, path, operation.Status)
			}
			if operation.OperationID == "" {
				t.Fatalf("OpenAPI operation %s %s has no operationId", method, path)
			}
			if prior, exists := operationIDs[operation.OperationID]; exists {
				t.Fatalf("duplicate operationId %s at %s and %s", operation.OperationID, prior, path)
			}
			key := method + " " + path
			want[key] = operation.OperationID
			operationIDs[operation.OperationID] = key
		}
	}

	router := newCompleteManagementRouter(t)
	got := make(map[string]struct{})
	for pattern, group := range router.groups {
		path := strings.TrimPrefix(pattern, "/api/admin/v1")
		for method := range group.routes {
			got[method+" "+path] = struct{}{}
		}
	}
	if len(want) != 61 || len(got) != len(want) {
		t.Fatalf("OpenAPI operations=%d routes=%d, want 61", len(want), len(got))
	}
	for key := range want {
		if _, exists := got[key]; !exists {
			t.Errorf("implemented OpenAPI operation has no route: %s", key)
		}
	}
	for key := range got {
		if _, exists := want[key]; !exists {
			t.Errorf("management route has no implemented OpenAPI operation: %s", key)
		}
	}
}

func newCompleteManagementRouter(t *testing.T) *Router {
	t.Helper()
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
	return router
}

type openAPIOperation struct {
	OperationID string `yaml:"operationId"`
	Status      string `yaml:"x-status"`
}

type openAPIPath struct {
	Get    *openAPIOperation `yaml:"get"`
	Post   *openAPIOperation `yaml:"post"`
	Patch  *openAPIOperation `yaml:"patch"`
	Delete *openAPIOperation `yaml:"delete"`
}

func (p openAPIPath) operations() map[string]*openAPIOperation {
	return map[string]*openAPIOperation{"GET": p.Get, "POST": p.Post, "PATCH": p.Patch, "DELETE": p.Delete}
}

func errorsIsNotExist(err error) bool { return err != nil && os.IsNotExist(err) }

func TestNewManagementRouterRejectsPartialSurface(t *testing.T) {
	if _, err := NewManagementRouter(ManagementOptions{}); err == nil {
		t.Fatal("partial management surface was accepted")
	}
}
