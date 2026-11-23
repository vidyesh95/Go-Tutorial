package httpapi

import (
	"net/http"
	"time"
)

const (
	sessionCookieName = "taskapi_session"

	// The OAuth state cookie only needs to survive the round trip to GitHub and back.
	oauthCookieName = "taskapi_oauth"
	oauthCookieTTL  = 10 * time.Minute
	oauthCookiePath = "/v1/session-auth/oauth/github"
)

// cookieConfig builds the session cookie. Every attribute is a deliberate defence:
//
//   - HttpOnly: JavaScript cannot read the cookie, so an XSS bug cannot steal the session token
//     (it can still make requests as the user, but cannot take the token away).
//   - Secure: sent over HTTPS only, so it cannot leak over plain http. The one legitimate reason
//     to turn it off is local development over http://localhost (TASKAPI_COOKIE_SECURE=false);
//     browsers treat localhost as secure anyway, curl and Safari may not.
//   - SameSite=Lax: the browser omits the cookie from cross-site subrequests and form POSTs (the
//     CSRF vectors) but still sends it on top-level navigations, so following a link to the site
//     keeps you logged in. Strict would break that. Lax is the browser default nowadays; we set
//     it explicitly because defaults differ between browsers and versions.
//   - Path=/ and no Domain: host-only cookie for exactly this origin, never shared with sibling
//     subdomains. (Hardening step: name it "__Host-taskapi_session"; browsers then enforce
//     Secure, Path=/ and no Domain for you. It is not the default here because __Host- cookies
//     are rejected over http, which would break the local curl walkthrough.)
//   - Max-Age = the session TTL, so the browser forgets it when the server would.
type cookieConfig struct {
	Name   string
	Secure bool
	TTL    time.Duration
}

func (c cookieConfig) set(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: c.Name, Value: token, Path: "/", MaxAge: int(c.TTL.Seconds()),
		HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteLaxMode,
	})
}

// clear tells the browser to delete the cookie (Max-Age < 0 emits "Max-Age=0").
func (c cookieConfig) clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: c.Name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteLaxMode,
	})
}

// setOAuthState stores "<state>.<pkce verifier>" for the callback to compare against. Neither
// half contains a '.', so the split is unambiguous. SameSite must be Lax (not Strict): the
// callback is a cross-site top-level navigation FROM GitHub, and Strict cookies are withheld
// from those.
func (c cookieConfig) setOAuthState(w http.ResponseWriter, state, verifier string) {
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookieName, Value: state + "." + verifier, Path: oauthCookiePath,
		MaxAge: int(oauthCookieTTL.Seconds()), HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteLaxMode,
	})
}

func (c cookieConfig) clearOAuthState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oauthCookieName, Value: "", Path: oauthCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: c.Secure, SameSite: http.SameSiteLaxMode,
	})
}
