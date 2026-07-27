package adminapi

import (
	"fmt"
	"time"
)

type ManagementEvents interface {
	EventSubscriber
	SessionEventSink
}

// ManagementOptions is the complete operation surface for /api/admin/v1.
// Requiring every dependency prevents a deployment from silently exposing a
// partially registered management API.
type ManagementOptions struct {
	Middleware   MiddlewareOptions
	CookieSecure bool
	SSEHeartbeat time.Duration

	Auth          AuthOperations
	Users         AdminUserService
	Audit         AuditReader
	Overview      OverviewReader
	Groups        GroupOperations
	Settings      SettingsOperations
	JoinRequests  JoinRequestOperations
	ScheduledJobs ScheduledJobOperations
	Knowledge     KnowledgeOperations
	Analytics     AnalyticsOperations
	Commands      CommandOperations
	System        SystemOperations
	Events        ManagementEvents
}

type routeRegistrar interface {
	Register(*Router) error
}

func NewManagementRouter(options ManagementOptions) (*Router, error) {
	if options.Auth == nil || options.Users == nil || options.Audit == nil || options.Overview == nil ||
		options.Groups == nil || options.Settings == nil || options.JoinRequests == nil || options.ScheduledJobs == nil ||
		options.Knowledge == nil || options.Analytics == nil || options.Commands == nil || options.System == nil || options.Events == nil {
		return nil, fmt.Errorf("complete admin operation dependencies are required")
	}
	options.Middleware.Authenticator = options.Auth
	router, err := NewRouter(options.Middleware)
	if err != nil {
		return nil, fmt.Errorf("create admin router: %w", err)
	}

	authHandlers, err := NewAuthHandlers(options.Auth, options.Events, options.CookieSecure)
	if err != nil {
		return nil, err
	}
	userHandlers, err := NewUsersHandlers(options.Users, options.Events, options.CookieSecure)
	if err != nil {
		return nil, err
	}
	auditHandlers, err := NewAuditHandlers(options.Audit)
	if err != nil {
		return nil, err
	}
	overviewHandler, err := NewOverviewHandler(options.Overview)
	if err != nil {
		return nil, err
	}
	groupHandlers, err := NewGroupHandlers(options.Groups)
	if err != nil {
		return nil, err
	}
	settingsHandlers, err := NewSettingsHandlers(options.Settings)
	if err != nil {
		return nil, err
	}
	joinRequestHandlers, err := NewJoinRequestHandlers(options.JoinRequests)
	if err != nil {
		return nil, err
	}
	scheduledJobHandlers, err := NewScheduledJobHandlers(options.ScheduledJobs)
	if err != nil {
		return nil, err
	}
	knowledgeHandlers, err := NewKnowledgeHandlers(options.Knowledge)
	if err != nil {
		return nil, err
	}
	analyticsHandlers, err := NewAnalyticsHandlers(options.Analytics)
	if err != nil {
		return nil, err
	}
	commandHandlers, err := NewCommandHandlers(options.Commands)
	if err != nil {
		return nil, err
	}
	systemHandlers, err := NewSystemHandlers(options.System)
	if err != nil {
		return nil, err
	}
	eventsHandler, err := NewEventsHandler(options.Events, options.SSEHeartbeat)
	if err != nil {
		return nil, err
	}

	registrars := []routeRegistrar{
		authHandlers, userHandlers, auditHandlers, overviewHandler, groupHandlers, settingsHandlers,
		joinRequestHandlers, scheduledJobHandlers, knowledgeHandlers, analyticsHandlers,
		commandHandlers, systemHandlers, eventsHandler,
	}
	for _, registrar := range registrars {
		if err := registrar.Register(router); err != nil {
			return nil, fmt.Errorf("register admin routes: %w", err)
		}
	}
	return router, nil
}
