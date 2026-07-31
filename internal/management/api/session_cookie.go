package adminapi

import (
	"net/http"
	"time"
)

func setSessionCookie(w http.ResponseWriter, r *http.Request, credential string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: credential, Path: "/api/admin/v1", HttpOnly: true,
		Secure: secureCookieFromContext(r.Context()), SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/api/admin/v1", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: secureCookieFromContext(r.Context()), SameSite: http.SameSiteStrictMode,
	})
}
