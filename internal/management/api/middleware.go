package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/zjutjh/jxh-go/internal/management/auth"
)

const SessionCookieName = "jxh_admin_session"

const defaultMaxConcurrentRequests = 128

type Authenticator interface {
	Authenticate(ctx context.Context, credential string) (auth.AuthContext, error)
}

type ReplacementAuthenticator interface {
	AuthenticateForRotation(ctx context.Context, credential string) (auth.AuthContext, error)
}

type MiddlewareOptions struct {
	TrustedProxies        []string
	MaxBodyBytes          int64
	MaxConcurrentRequests int
	Random                io.Reader
	Logger                *log.Logger
	Authenticator         Authenticator
}

type middleware struct {
	trusted       []netip.Prefix
	maxBodyBytes  int64
	requestSlots  chan struct{}
	random        io.Reader
	logger        *log.Logger
	authenticator Authenticator
}

func newMiddleware(options MiddlewareOptions) (*middleware, error) {
	trusted, err := parseTrustedProxies(options.TrustedProxies)
	if err != nil {
		return nil, err
	}
	if options.MaxBodyBytes <= 0 {
		return nil, fmt.Errorf("admin max body bytes must be positive")
	}
	if options.MaxConcurrentRequests == 0 {
		options.MaxConcurrentRequests = defaultMaxConcurrentRequests
	}
	if options.MaxConcurrentRequests < 0 {
		return nil, fmt.Errorf("admin max concurrent requests must be positive")
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Logger == nil {
		options.Logger = log.New(io.Discard, "", 0)
	}
	return &middleware{
		trusted: trusted, maxBodyBytes: options.MaxBodyBytes,
		requestSlots: make(chan struct{}, options.MaxConcurrentRequests),
		random:       options.Random, logger: options.Logger, authenticator: options.Authenticator,
	}, nil
}

func (m *middleware) base(next http.Handler) http.Handler {
	return m.recoverPanic(m.requestID(m.securityHeaders(m.limitConcurrency(m.bodyBoundary(m.clientIP(next))))))
}

func (m *middleware) limitConcurrency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case m.requestSlots <- struct{}{}:
			defer func() { <-m.requestSlots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeAPIError(w, r, http.StatusServiceUnavailable, CodeServerBusy, "管理服务当前繁忙", nil, true)
		}
	})
}

func (m *middleware) route(options RouteOptions, next http.Handler) http.Handler {
	if options.Permission != "" {
		next = m.permission(options.Permission, next)
	}
	if options.CSRF {
		next = m.csrf(next)
	}
	if !options.Public {
		next = m.authenticate(options.AllowReplacedAuth, next)
	}
	if options.Mutation {
		next = m.origin(next)
	}
	return next
}

func (m *middleware) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() == nil {
				return
			}
			requestID := w.Header().Get("X-Request-ID")
			m.logger.Printf("admin request panic request_id=%s", requestID)
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: Error{
				Code: CodeInternal, Message: "服务器内部错误", RequestID: requestID, Fields: map[string][]string{}, Retryable: false,
			}})
		}()
		next.ServeHTTP(w, r)
	})
}

func (m *middleware) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw [16]byte
		if _, err := io.ReadFull(m.random, raw[:]); err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: Error{
				Code: CodeInternal, Message: "服务器内部错误", RequestID: "req_unavailable", Fields: map[string][]string{}, Retryable: false,
			}})
			return
		}
		requestID := "req_" + base64.RawURLEncoding.EncodeToString(raw[:])
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey, requestID)))
	})
}

func (m *middleware) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (m *middleware) bodyBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > m.maxBodyBytes {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "请求体过大", nil, false)
			return
		}
		hasBody := r.ContentLength != 0 || len(r.TransferEncoding) > 0
		if hasBody {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeAPIError(w, r, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, "请求体必须使用 application/json", nil, false)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, m.maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func (m *middleware) clientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := resolveClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), m.trusted)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPContextKey, clientIP)))
	})
}

func (m *middleware) authenticate(allowReplaced bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.authenticator == nil {
			writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
			return
		}
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
			return
		}
		identity, err := m.authenticator.Authenticate(r.Context(), cookie.Value)
		if errors.Is(err, auth.ErrUnauthenticated) && allowReplaced {
			if replacement, ok := m.authenticator.(ReplacementAuthenticator); ok {
				identity, err = replacement.AuthenticateForRotation(r.Context(), cookie.Value)
			}
		}
		if err != nil {
			if !errors.Is(err, auth.ErrUnauthenticated) {
				m.logger.Printf("admin authentication unavailable request_id=%s", RequestIDFromContext(r.Context()))
				w.Header().Set("Retry-After", "3")
				writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "authentication service is unavailable", nil, true)
				return
			}
			writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "登录状态无效或已过期", nil, false)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey, identity)))
	})
}

func (m *middleware) origin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, err := canonicalOrigin(r.Header.Get("Origin"))
		expected, expectedErr := requestOrigin(r)
		if err != nil || expectedErr != nil || origin != expected {
			writeAPIError(w, r, http.StatusForbidden, CodeOriginForbidden, "请求来源不受信任", nil, false)
			return
		}
		secure := strings.HasPrefix(origin, "https://")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), secureCookieContextKey, secure)))
	})
}

func requestOrigin(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("request is required")
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme != "http" && scheme != "https" {
		return "", errors.New("forwarded scheme is required")
	}
	return canonicalOrigin(scheme + "://" + r.Host)
}

func (m *middleware) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := AuthFromContext(r.Context())
		if !ok || !validCSRF(r.Header.Get("X-CSRF-Token"), identity.CSRFToken) {
			writeAPIError(w, r, http.StatusForbidden, CodeCSRFInvalid, "CSRF 校验失败", nil, false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *middleware) permission(permission auth.Permission, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := AuthFromContext(r.Context())
		if !ok || !auth.Allowed(identity.User.Role, permission) {
			writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "没有执行此操作的权限", nil, false)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func canonicalOrigin(value string) (string, error) {
	if value == "" || value == "null" {
		return "", errors.New("origin is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", errors.New("invalid origin host")
	}
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return parsed.Scheme + "://" + host, nil
}

func parseTrustedProxies(values []string) ([]netip.Prefix, error) {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, addressErr := netip.ParseAddr(value)
			if addressErr != nil {
				return nil, fmt.Errorf("invalid trusted proxy")
			}
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

func resolveClientIP(remoteAddress, forwarded string, trusted []netip.Prefix) string {
	peer := parseRemoteIP(remoteAddress)
	if !peer.IsValid() {
		return ""
	}
	if !isTrusted(peer, trusted) || forwarded == "" {
		return peer.String()
	}
	parts := strings.Split(forwarded, ",")
	chain := make([]netip.Addr, 0, len(parts)+1)
	for _, part := range parts {
		address, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return peer.String()
		}
		chain = append(chain, address.Unmap())
	}
	chain = append(chain, peer)
	for index := len(chain) - 1; index >= 0; index-- {
		if !isTrusted(chain[index], trusted) {
			return chain[index].String()
		}
	}
	return chain[0].String()
}

func parseRemoteIP(value string) netip.Addr {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		host = value
	}
	address, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}
	}
	return address.Unmap()
}

func isTrusted(address netip.Addr, trusted []netip.Prefix) bool {
	for _, prefix := range trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
