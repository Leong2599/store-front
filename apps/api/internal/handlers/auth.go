package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/markbates/goth/gothic"
	"gorm.io/gorm"

	"golang.org/x/crypto/bcrypt"

	"store-front/apps/api/internal/config"
	"store-front/apps/api/internal/mail"
	"store-front/apps/api/internal/models"
	"store-front/apps/api/internal/services"
	"store-front/apps/api/internal/totp"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	DB          *gorm.DB
	AuthService *services.AuthService
	Config      *config.Config
	// Mailer is optional. Without it the reset link is logged instead of sent,
	// which is what you want in dev and must never be what happens in prod —
	// ForgotPassword refuses to log the token when APP_ENV is production.
	Mailer *mail.Mailer
}

// AuthResponse documents the body returned by register, login and refresh.
//
// The handlers emit gin.H rather than this struct, so nothing enforces the two
// agree — if you change what an auth handler writes, change this with it. It
// exists because a reference that says "No Body" is worse than no reference.
type AuthResponse struct {
	Data struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresAt    int64  `json:"expires_at"`
		} `json:"tokens"`
		User models.User `json:"user"`
	} `json:"data"`
	Message string `json:"message"`
}

// The types below exist so the API reference can show a body instead of
// "No Body". The handlers emit gin.H maps, so nothing enforces that these stay
// in step — if you change what a handler writes, change its type here too.
// They are documentation with a compiler attached, which is still better than
// prose nobody updates.

// MessageResponse is the plain acknowledgement shape.
type MessageResponse struct {
	Message string `json:"message"`
}

// IssuedKeyResponse is returned once, when an API key is created.
type IssuedKeyResponse struct {
	Data struct {
		Key   models.APIKey `json:"key"`
		Token string        `json:"token"`
	} `json:"data"`
	Message string `json:"message"`
}

// TOTPStatusResponse describes the caller's two-factor state.
type TOTPStatusResponse struct {
	Data struct {
		Enabled              bool  `json:"enabled"`
		BackupCodesRemaining int   `json:"backup_codes_remaining"`
		TrustedDevices       int64 `json:"trusted_devices"`
	} `json:"data"`
}

// PresignResponse carries the URL a browser PUTs to, and the key to send back
// to /uploads/complete afterwards.
type PresignResponse struct {
	Data struct {
		PresignedURL string `json:"presigned_url"`
		Key          string `json:"key"`
	} `json:"data"`
}

// ChainStatusResponse is the activity-log integrity verdict.
type ChainStatusResponse struct {
	Valid        bool   `json:"valid"`
	TotalEntries int    `json:"total_entries"`
	BrokenAt     int    `json:"broken_at,omitempty"`
	BrokenAtID   string `json:"broken_at_id,omitempty"`
	Expected     string `json:"expected,omitempty"`
	Got          string `json:"got,omitempty"`
	Message      string `json:"message,omitempty"`
}

// ErrorResponse is the error envelope every endpoint uses.
type ErrorResponse struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Details map[string]string `json:"details,omitempty"`
	} `json:"error"`
}

type RegisterRequest struct {
	FirstName  string `json:"first_name" binding:"required,min=2"`
	LastName   string `json:"last_name" binding:"required,min=2"`
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=8"`
	MACAddress string `json:"mac_address"` // optional — provided by client if available
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type ForgotPasswordRequest struct {
	Email string `json:"email" binding:"required,email"`
}

type ResetPasswordRequest struct {
	Token    string `json:"token" binding:"required"`
	Password string `json:"password" binding:"required,min=8"`
}

// Register creates a new user account.
func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	// Check email uniqueness
	var existingUser models.User
	if err := h.DB.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error": gin.H{
				"code":    "EMAIL_EXISTS",
				"message": "A user with this email already exists",
			},
		})
		return
	}

	user := models.User{
		FirstName:  req.FirstName,
		LastName:   req.LastName,
		Email:      req.Email,
		Password:   req.Password,
		Role:       models.RoleUser,
		Active:     true,
		IPAddress:  c.ClientIP(),
		MACAddress: req.MACAddress,
	}

	if err := h.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to create user",
			},
		})
		return
	}

	// Off the request path: signup should not wait on SMTP, and a mail failure
	// must not fail an account that was created successfully.
	go h.deliverVerificationEmail(user)

	tokens, err := h.AuthService.GenerateTokenPair(user.ID, user.Email, user.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "TOKEN_ERROR",
				"message": "Failed to generate tokens",
			},
		})
		return
	}

	// Record the refresh token as a server-side session so it can be revoked.
	if _, err := services.CreateSession(h.DB, c, user.ID, tokens.RefreshToken); err != nil {
		log.Printf("auth: failed to record session for %s: %v", user.ID, err)
	}

	// Set HttpOnly auth cookies for browser clients.
	h.AuthService.SetAuthCookies(c, tokens)

	// v3.30.1: emit a semantic activity row so /system/activity reflects
	// the signup. Non-blocking — a logging failure won't fail the
	// register request.
	services.LogRegister(h.DB, c, user.ID, user.Email)

	c.JSON(http.StatusCreated, gin.H{
		"data": gin.H{
			"user":   user,
			"tokens": tokens,
		},
		"message": "User registered successfully",
	})
}

// Login authenticates a user and returns tokens.
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	var user models.User
	if err := h.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		// v3.30.1: unknown email is the most common brute-force fingerprint;
		// surface it in /system/activity as "warn" severity so operators
		// can spot credential-stuffing spikes.
		services.LogLoginFailed(h.DB, c, req.Email)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "INVALID_CREDENTIALS",
				"message": "Invalid email or password",
			},
		})
		return
	}

	if !user.Active {
		services.LogActivity(h.DB, c, services.ActivityArgs{
			Action:       "auth.login_blocked",
			Severity:     "warn",
			Summary:      "Sign-in blocked for disabled account " + user.Email,
			ResourceType: "user",
			ResourceID:   user.ID,
		})
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"code":    "ACCOUNT_DISABLED",
				"message": "Your account has been disabled",
			},
		})
		return
	}

	// Locked accounts are refused before the password is even compared, so a
	// lockout cannot be probed by timing the comparison.
	if user.LockedUntil != nil && time.Now().Before(*user.LockedUntil) {
		remaining := time.Until(*user.LockedUntil).Round(time.Minute)
		if remaining < time.Minute {
			remaining = time.Minute
		}
		services.LogActivity(h.DB, c, services.ActivityArgs{
			Action:       "auth.login_locked",
			Severity:     "warn",
			Summary:      "Sign-in refused: account is temporarily locked",
			ResourceType: "user",
			ResourceID:   user.ID,
		})
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error": gin.H{
				"code":    "ACCOUNT_LOCKED",
				"message": fmt.Sprintf("Too many failed attempts. Try again in about %d minute(s), or reset your password.", int(remaining.Minutes())),
			},
		})
		return
	}

	// Opt-in gate. Social and SSO sign-ins are unaffected — the IdP already
	// proved the address, and those paths set EmailVerifiedAt on first login.
	if h.Config.RequireEmailVerification && user.EmailVerifiedAt == nil && user.Password != "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"code":    "EMAIL_NOT_VERIFIED",
				"message": "Confirm your email address before signing in. Check your inbox for the link.",
			},
		})
		return
	}

	if user.Password == "" {
		provider := user.Provider
		if provider == "" || provider == "local" {
			provider = "social login"
		}
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "SOCIAL_AUTH_ONLY",
				"message": fmt.Sprintf("This account uses %s. Please sign in with your social account.", provider),
			},
		})
		return
	}

	if !user.CheckPassword(req.Password) {
		// Wrong password on a real account — distinct from "unknown email"
		// because Sentinel's brute-force heuristics weight these higher.
		services.LogLoginFailed(h.DB, c, req.Email)
		h.registerFailedLogin(&user)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "INVALID_CREDENTIALS",
				"message": "Invalid email or password",
			},
		})
		return
	}

	// Any successful password check clears the counter, including one that
	// still has 2FA ahead of it — the password was correct, which is what this
	// counter measures.
	if user.FailedLoginCount > 0 || user.LockedUntil != nil {
		h.DB.Model(&models.User{}).Where("id = ?", user.ID).
			Updates(map[string]interface{}{"failed_login_count": 0, "locked_until": nil})
	}

	// Check if user has TOTP enabled
	var totpConfig models.TwoFactorConfig
	if err := h.DB.Where("user_id = ? AND enabled = ?", user.ID, true).First(&totpConfig).Error; err == nil {
		// TOTP is enabled — check for trusted device
		if !IsTrustedDevice(c, h.DB, user.ID) {
			// Generate a short-lived pending token for TOTP verification
			pendingToken, err := totp.GeneratePendingToken()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{"code": "TOKEN_ERROR", "message": "Failed to create verification session"},
				})
				return
			}

			// Store hashed pending token in DB
			h.DB.Create(&models.TOTPPendingToken{
				UserID:    user.ID,
				TokenHash: totp.HashToken(pendingToken),
				ExpiresAt: time.Now().Add(totp.PendingTokenExpiry),
			})

			c.JSON(http.StatusOK, gin.H{
				"data": gin.H{
					"totp_required": true,
					"pending_token": pendingToken,
				},
				"message": "Two-factor authentication required",
			})
			return
		}
	}

	tokens, err := h.AuthService.GenerateTokenPair(user.ID, user.Email, user.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "TOKEN_ERROR",
				"message": "Failed to generate tokens",
			},
		})
		return
	}

	// Set HttpOnly auth cookies for browser clients. Native mobile/desktop
	// clients ignore them and continue to use the Bearer header from the
	// tokens object below — both flows work.
	//
	// Record the refresh token as a server-side session so this device can be
	// listed and revoked later.
	if _, err := services.CreateSession(h.DB, c, user.ID, tokens.RefreshToken); err != nil {
		log.Printf("auth: failed to record session for %s: %v", user.ID, err)
	}
	h.AuthService.SetAuthCookies(c, tokens)

	// v3.30.1: successful sign-in lands in /system/activity at info
	// severity. IP + user-agent come from the request context inside
	// LogLogin so brute-force investigation has the full pair.
	services.LogLogin(h.DB, c, user.ID, user.Email)

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"user":   user,
			"tokens": tokens,
		},
		"message": "Logged in successfully",
	})
}

// Refresh generates a new access token from a refresh token. The token is
// read from the grit_refresh cookie first (web client) and falls back to
// the JSON body (mobile/desktop bearer clients) — so a single endpoint
// supports both flows.
func (h *AuthHandler) Refresh(c *gin.Context) {
	refreshToken := ""
	if cookieValue, err := c.Cookie("grit_refresh"); err == nil && cookieValue != "" {
		refreshToken = cookieValue
	} else {
		var req RefreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": gin.H{
					"code":    "VALIDATION_ERROR",
					"message": err.Error(),
				},
			})
			return
		}
		refreshToken = req.RefreshToken
	}

	claims, err := h.AuthService.ValidateToken(refreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "INVALID_TOKEN",
				"message": "Invalid or expired refresh token",
			},
		})
		return
	}

	// Re-verify the account on every refresh. A stateless refresh token is
	// otherwise valid for its full lifetime even after the user is deleted or
	// deactivated; re-loading the user closes that window and lets a role
	// change take effect on the next refresh (partial revocation without a
	// server-side token store).
	var user models.User
	if err := h.DB.First(&user, "id = ?", claims.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "INVALID_TOKEN",
				"message": "Account no longer exists",
			},
		})
		return
	}
	if !user.Active {
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"code":    "ACCOUNT_DISABLED",
				"message": "This account has been disabled",
			},
		})
		return
	}

	tokens, err := h.AuthService.GenerateTokenPair(user.ID, user.Email, user.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "TOKEN_ERROR",
				"message": "Failed to generate tokens",
			},
		})
		return
	}

	// Refresh the HttpOnly cookies so the new access token lands in the
	// browser without any JS handling. The bearer JSON path is unchanged
	// for native clients.
	//
	// Rotate the session. This is where revocation actually bites: a session
	// that was revoked, idled out, aged past its absolute limit, or whose token
	// was replayed after rotation has no live row, and the refresh is refused.
	if _, err := services.RotateSession(h.DB, c, refreshToken, tokens.RefreshToken); err != nil {
		h.AuthService.ClearAuthCookies(c)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "SESSION_REVOKED",
				"message": "This session is no longer valid. Please sign in again.",
			},
		})
		return
	}
	h.AuthService.SetAuthCookies(c, tokens)

	c.JSON(http.StatusOK, gin.H{
		"data": gin.H{
			"tokens": tokens,
		},
		"message": "Token refreshed successfully",
	})
}

// Logout invalidates the user's session. Cookies are cleared immediately;
// native bearer clients should also drop their stored tokens client-side.
func (h *AuthHandler) Logout(c *gin.Context) {
	// v3.30.1: read the user out of context BEFORE clearing cookies so
	// the activity row carries the right email. The auth middleware set
	// "user" on the gin context when the request came in.
	var actorID, actorEmail string
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(models.User); ok {
			actorID = u.ID
			actorEmail = u.Email
		}
	}

	// Revoke the server-side session BEFORE clearing cookies — once they're
	// gone we can no longer identify which session to kill. This is what makes
	// logout real: the refresh token is dead immediately, not merely forgotten
	// by this browser.
	if rt, err := c.Cookie("grit_refresh"); err == nil && rt != "" {
		if err := services.RevokeSessionByToken(h.DB, rt); err != nil {
			log.Printf("auth: failed to revoke session on logout: %v", err)
		}
	}

	h.AuthService.ClearAuthCookies(c)

	if actorID != "" {
		services.LogLogout(h.DB, c, actorID, actorEmail)
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "Logged out successfully",
	})
}

// Me returns the current authenticated user.
func (h *AuthHandler) Me(c *gin.Context) {
	user, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": gin.H{
				"code":    "UNAUTHORIZED",
				"message": "Not authenticated",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": user,
	})
}

// ForgotPassword initiates a password reset.
func (h *AuthHandler) ForgotPassword(c *gin.Context) {
	var req ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	// One response for every outcome. Any variation — a different message, a
	// different status, a measurably different latency — turns this endpoint
	// into an oracle for which email addresses hold accounts.
	const genericResponse = "If an account with that email exists, a password reset link has been sent"

	var user models.User
	if err := h.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"message": genericResponse})
		return
	}

	// Everything past the lookup — minting the token, storing it, delivering the
	// link — runs off the request path. Both branches then do the same work
	// before answering (parse, one indexed SELECT), so a registered address does
	// not take measurably longer to respond than an unregistered one. Identical
	// wording with a distinguishable response time is still an oracle.
	//
	// c.ClientIP() is read here: the gin context must not be touched once the
	// handler has returned.
	go h.deliverPasswordReset(user, c.ClientIP())

	c.JSON(http.StatusOK, gin.H{"message": genericResponse})
}

// deliverPasswordReset issues a reset token and sends the link. It runs in its
// own goroutine, so it owns its context and reports failures only to the log —
// there is no caller left to tell, and telling the original one would have
// confirmed the address exists.
func (h *AuthHandler) deliverPasswordReset(user models.User, clientIP string) {
	token, err := services.GenerateResetToken()
	if err != nil {
		log.Printf("password reset: generating token for %s: %v", user.Email, err)
		return
	}

	if _, err := services.CreatePasswordResetToken(h.DB, user.ID, token, clientIP); err != nil {
		log.Printf("password reset: storing token for %s: %v", user.Email, err)
		return
	}

	resetURL := strings.TrimSuffix(h.Config.OAuthFrontendURL, "/") + "/reset-password?token=" + url.QueryEscape(token)

	if h.Mailer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := h.Mailer.Send(ctx, mail.SendOptions{
			To:       user.Email,
			Subject:  "Reset your password",
			Template: "password-reset",
			Data: map[string]interface{}{
				"AppName":  h.Config.AppName,
				"Title":    "Reset your password",
				"Message":  "We received a request to reset your password. This link expires in one hour and can only be used once. If you didn't ask for this, you can ignore this email.",
				"ResetURL": resetURL,
				"Year":     time.Now().Year(),
			},
		}); err != nil {
			log.Printf("password reset: sending email to %s: %v", user.Email, err)
		}
		return
	}

	if h.Config.AppEnv == "production" {
		// No mailer in production means nobody can complete a reset. Say so
		// loudly rather than printing a working token into the log — a live
		// reset link in a log file is a credential.
		log.Printf("password reset: NO MAILER CONFIGURED: %s cannot receive a reset link. Set RESEND_API_KEY.", user.Email)
		return
	}

	// Dev convenience only, and only outside production.
	log.Printf("password reset link for %s: %s", user.Email, resetURL)
}

// Unlock clears a lockout early. Waiting out the window is the normal path;
// this exists for the support call that follows a user locking themselves out
// five minutes before a demo.
func (h *UserHandler) Unlock(c *gin.Context) {
	id := c.Param("id")

	res := h.DB.Model(&models.User{}).Where("id = ?", id).
		Updates(map[string]interface{}{"locked_until": nil, "failed_login_count": 0})
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{"code": "INTERNAL_ERROR", "message": "Failed to unlock the account"},
		})
		return
	}
	if res.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"code": "NOT_FOUND", "message": "User not found"},
		})
		return
	}

	services.LogActivity(h.DB, c, services.ActivityArgs{
		Action:       "user.unlock",
		Severity:     "warn",
		Summary:      "Account lockout cleared by an administrator",
		ResourceType: "user",
		ResourceID:   id,
	})

	c.JSON(http.StatusOK, gin.H{"message": "Account unlocked"})
}

// registerFailedLogin counts a wrong password against the account and locks it
// once the threshold is reached.
//
// Only wrong-password-on-a-real-account is counted. Counting unknown emails
// would let anyone lock an address they can guess, which turns a defence into
// a denial-of-service tool.
//
// The increment is a single UPDATE rather than read-modify-write, so parallel
// attempts cannot each read the same count and overwrite one another.
func (h *AuthHandler) registerFailedLogin(user *models.User) {
	max := h.Config.LoginMaxAttempts
	if max <= 0 {
		return // lockout disabled
	}

	if err := h.DB.Model(&models.User{}).
		Where("id = ?", user.ID).
		UpdateColumn("failed_login_count", gorm.Expr("failed_login_count + 1")).Error; err != nil {
		log.Printf("lockout: incrementing failed_login_count for %s: %v", user.ID, err)
		return
	}

	var fresh models.User
	if err := h.DB.Select("id", "failed_login_count").First(&fresh, "id = ?", user.ID).Error; err != nil {
		return
	}
	if fresh.FailedLoginCount < max {
		return
	}

	until := time.Now().Add(h.Config.LoginLockoutWindow)
	if err := h.DB.Model(&models.User{}).
		Where("id = ?", user.ID).
		Updates(map[string]interface{}{"locked_until": until, "failed_login_count": 0}).Error; err != nil {
		log.Printf("lockout: locking %s: %v", user.ID, err)
		return
	}
	log.Printf("lockout: %s locked until %s after %d failed attempts", user.Email, until.Format(time.RFC3339), max)
}

// SendVerificationEmail issues a fresh verification link for the signed-in
// user. Authenticated on purpose: an unauthenticated "send a link to this
// address" endpoint is a spam cannon aimed at whoever you name.
func (h *AuthHandler) SendVerificationEmail(c *gin.Context) {
	userID := c.GetString("user_id")

	var user models.User
	if err := h.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"code": "NOT_FOUND", "message": "User not found"},
		})
		return
	}

	if user.EmailVerifiedAt != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"code": "ALREADY_VERIFIED", "message": "This email is already verified"},
		})
		return
	}

	go h.deliverVerificationEmail(user)

	c.JSON(http.StatusOK, gin.H{
		"message": "Verification email sent. The link is valid for 48 hours.",
	})
}

// The token from a verification link.
type VerifyEmailRequest struct {
	Token string `json:"token" binding:"required"`
}

// VerifyEmail consumes a verification token. Public — the user clicks this
// from their mail client, where they are usually not signed in.

func (h *AuthHandler) VerifyEmail(c *gin.Context) {
	var req VerifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{"code": "VALIDATION_ERROR", "message": err.Error()},
		})
		return
	}

	if _, err := services.ConsumeEmailVerificationToken(h.DB, req.Token); err != nil {
		// One message for expired, spent, unknown and address-changed. Telling
		// them apart tells an attacker which tokens once existed.
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "INVALID_TOKEN",
				"message": "That verification link is invalid or has expired. Request a new one.",
			},
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Email verified"})
}

// deliverVerificationEmail mints a token and sends the link, off the request
// path so a slow SMTP call cannot hold the response open.
func (h *AuthHandler) deliverVerificationEmail(user models.User) {
	token, err := services.GenerateVerificationToken()
	if err != nil {
		log.Printf("email verification: generating token for %s: %v", user.Email, err)
		return
	}

	if _, err := services.CreateEmailVerificationToken(h.DB, user.ID, user.Email, token); err != nil {
		log.Printf("email verification: storing token for %s: %v", user.Email, err)
		return
	}

	verifyURL := strings.TrimSuffix(h.Config.OAuthFrontendURL, "/") + "/verify-email?token=" + url.QueryEscape(token)

	if h.Mailer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := h.Mailer.Send(ctx, mail.SendOptions{
			To:       user.Email,
			Subject:  "Confirm your email address",
			Template: "email-verification",
			Data: map[string]interface{}{
				"AppName":   h.Config.AppName,
				"Title":     "Confirm your email address",
				"Message":   "Click the button below to confirm this address. The link expires in 48 hours and can only be used once.",
				"VerifyURL": verifyURL,
				"Year":      time.Now().Year(),
			},
		}); err != nil {
			log.Printf("email verification: sending to %s: %v", user.Email, err)
		}
		return
	}

	if h.Config.AppEnv == "production" {
		log.Printf("email verification: NO MAILER CONFIGURED: %s cannot receive a link. Set RESEND_API_KEY.", user.Email)
		return
	}

	log.Printf("email verification link for %s: %s", user.Email, verifyURL)
}

// ResetPassword resets a user's password with a valid token.
func (h *AuthHandler) ResetPassword(c *gin.Context) {
	var req ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": gin.H{
				"code":    "VALIDATION_ERROR",
				"message": err.Error(),
			},
		})
		return
	}

	// Consume first. The token is single-use and burning it before doing any
	// work means a failure later can't leave a still-valid token behind.
	userID, err := services.ConsumePasswordResetToken(h.DB, req.Token)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"code":    "INVALID_TOKEN",
				"message": "This reset link is invalid or has expired. Request a new one.",
			},
		})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to hash password",
			},
		})
		return
	}

	if err := h.DB.Model(&models.User{}).Where("id = ?", userID).
		Update("password", string(hashedPassword)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "Failed to update password",
			},
		})
		return
	}

	// The reason someone resets a password is to evict whoever they think is in
	// their account. Leaving that person's session alive would defeat the entire
	// exercise, so every device is signed out — including any the attacker holds.
	if err := services.RevokeAllUserSessions(h.DB, userID, ""); err != nil {
		log.Printf("password reset: revoking sessions for %s: %v", userID, err)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Password reset successfully. Please sign in with your new password.",
	})
}

// OAuthBegin redirects the user to the OAuth provider's consent screen.
func (h *AuthHandler) OAuthBegin(c *gin.Context) {
	provider := c.Param("provider")

	// Gothic reads provider from query string, not URL params
	q := c.Request.URL.Query()
	q.Set("provider", provider)
	c.Request.URL.RawQuery = q.Encode()

	gothic.BeginAuthHandler(c.Writer, c.Request)
}

// OAuthCallback completes the OAuth flow, finds or creates the user, and redirects with JWT tokens.
func (h *AuthHandler) OAuthCallback(c *gin.Context) {
	provider := c.Param("provider")

	q := c.Request.URL.Query()
	q.Set("provider", provider)
	c.Request.URL.RawQuery = q.Encode()

	gothUser, err := gothic.CompleteUserAuth(c.Writer, c.Request)
	if err != nil {
		log.Printf("OAuth callback error: %v", err)
		redirectURL := fmt.Sprintf("%s/login?error=%s", h.Config.OAuthFrontendURL, url.QueryEscape("Authentication failed. Please try again."))
		c.Redirect(http.StatusTemporaryRedirect, redirectURL)
		return
	}

	// Find or create user by email
	var user models.User
	result := h.DB.Where("email = ?", gothUser.Email).First(&user)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			// Create new user from OAuth data
			now := time.Now()
			user = models.User{
				FirstName:       gothUser.FirstName,
				LastName:        gothUser.LastName,
				Email:           gothUser.Email,
				Avatar:          gothUser.AvatarURL,
				Provider:        provider,
				Active:          true,
				EmailVerifiedAt: &now,
				IPAddress:       c.ClientIP(),
			}

			if provider == "google" {
				user.GoogleID = gothUser.UserID
			} else if provider == "github" {
				user.GithubID = gothUser.UserID
			}

			// If name is empty, try to use NickName
			if user.FirstName == "" && gothUser.NickName != "" {
				user.FirstName = gothUser.NickName
			}
			if user.FirstName == "" {
				user.FirstName = "User"
			}
			if user.LastName == "" {
				user.LastName = ""
			}

			if err := h.DB.Create(&user).Error; err != nil {
				log.Printf("OAuth: failed to create user: %v", err)
				redirectURL := fmt.Sprintf("%s/login?error=%s", h.Config.OAuthFrontendURL, url.QueryEscape("Failed to create account."))
				c.Redirect(http.StatusTemporaryRedirect, redirectURL)
				return
			}
		} else {
			log.Printf("OAuth: database error: %v", result.Error)
			redirectURL := fmt.Sprintf("%s/login?error=%s", h.Config.OAuthFrontendURL, url.QueryEscape("Something went wrong."))
			c.Redirect(http.StatusTemporaryRedirect, redirectURL)
			return
		}
	} else {
		// Link OAuth provider to existing account
		updates := map[string]interface{}{}
		if provider == "google" && user.GoogleID == "" {
			updates["google_id"] = gothUser.UserID
		} else if provider == "github" && user.GithubID == "" {
			updates["github_id"] = gothUser.UserID
		}
		if user.Avatar == "" && gothUser.AvatarURL != "" {
			updates["avatar"] = gothUser.AvatarURL
		}
		if user.Provider == "local" {
			updates["provider"] = provider
		}

		if len(updates) > 0 {
			h.DB.Model(&user).Updates(updates)
		}
	}

	if !user.Active {
		redirectURL := fmt.Sprintf("%s/login?error=%s", h.Config.OAuthFrontendURL, url.QueryEscape("Your account has been disabled."))
		c.Redirect(http.StatusTemporaryRedirect, redirectURL)
		return
	}

	// Generate JWT tokens
	tokens, err := h.AuthService.GenerateTokenPair(user.ID, user.Email, user.Role)
	if err != nil {
		log.Printf("OAuth: failed to generate tokens: %v", err)
		redirectURL := fmt.Sprintf("%s/login?error=%s", h.Config.OAuthFrontendURL, url.QueryEscape("Failed to sign in."))
		c.Redirect(http.StatusTemporaryRedirect, redirectURL)
		return
	}

	// Record the refresh token as a server-side session, so an OAuth login is
	// listed and revocable exactly like a password login.
	if _, err := services.CreateSession(h.DB, c, user.ID, tokens.RefreshToken); err != nil {
		log.Printf("OAuth: failed to record session for %s: %v", user.ID, err)
	}

	// Set HttpOnly auth cookies BEFORE redirecting so the browser stores
	// them as part of this same response. The callback page then just
	// navigates — no tokens in URL, no tokens in JS, no XSS exposure.
	h.AuthService.SetAuthCookies(c, tokens)

	// Redirect to frontend callback. No query params — tokens travel as
	// HttpOnly Set-Cookie headers on this 307 response.
	redirectURL := fmt.Sprintf("%s/auth/callback", h.Config.OAuthFrontendURL)
	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}
