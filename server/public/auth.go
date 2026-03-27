/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package public

import (
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/markdr-hue/HO/events"
	"github.com/markdr-hue/HO/security"
)

// authEndpointConfig holds the configuration for auto-generated auth routes.
type authEndpointConfig struct {
	ID             int
	TableName      string
	Path           string
	UsernameColumn string
	PasswordColumn string
	PublicColumns  []string
	JWTExpiryHours int
	DefaultRole    string
	RoleColumn     string
}

// loadAuthEndpoint loads an auth endpoint configuration from the per-site database.
func (h *Handler) loadAuthEndpoint(siteDB *sql.DB, path string) (*authEndpointConfig, error) {
	var ae authEndpointConfig
	var publicColsJSON string
	err := siteDB.QueryRow(
		"SELECT id, table_name, path, username_column, password_column, public_columns, jwt_expiry_hours, default_role, role_column FROM ho_auth_endpoints WHERE path = ?",
		path,
	).Scan(&ae.ID, &ae.TableName, &ae.Path, &ae.UsernameColumn, &ae.PasswordColumn, &publicColsJSON, &ae.JWTExpiryHours, &ae.DefaultRole, &ae.RoleColumn)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(publicColsJSON), &ae.PublicColumns)
	if ae.JWTExpiryHours == 0 {
		ae.JWTExpiryHours = 24
	}
	if ae.DefaultRole == "" {
		ae.DefaultRole = "user"
	}
	if ae.RoleColumn == "" {
		ae.RoleColumn = "role"
	}

	// Validate column names to prevent SQL injection via stored config.
	if err := security.ValidateColumnName(ae.UsernameColumn); err != nil {
		return nil, fmt.Errorf("invalid username_column in auth config: %w", err)
	}
	if err := security.ValidateColumnName(ae.PasswordColumn); err != nil {
		return nil, fmt.Errorf("invalid password_column in auth config: %w", err)
	}
	if err := security.ValidateColumnNames(ae.PublicColumns); err != nil {
		return nil, fmt.Errorf("invalid public_columns in auth config: %w", err)
	}
	if err := security.ValidateColumnName(ae.RoleColumn); err != nil {
		return nil, fmt.Errorf("invalid role_column in auth config: %w", err)
	}

	return &ae, nil
}

// handleAuthRequest handles /api/{path}/register, /api/{path}/login, /api/{path}/me.
// Returns true if the request was handled as an auth endpoint.
// readDB is the multi-connection read pool; writeDB is the single-connection write pool.
func (h *Handler) handleAuthRequest(w http.ResponseWriter, r *http.Request, siteID int, readDB *sql.DB, writeDB *sql.DB, fullPath string) bool {
	// Check if the path matches an auth endpoint pattern: {authPath}/{action}
	parts := strings.SplitN(fullPath, "/", 2)
	if len(parts) != 2 {
		return false
	}

	authPath := parts[0]
	action := parts[1]

	// Try to load auth endpoint config — uses read pool (no writes needed).
	ae, err := h.loadAuthEndpoint(readDB, authPath)
	if err != nil {
		return false // Not an auth endpoint, let DynamicAPI handle it.
	}

	physTable := ae.TableName

	switch action {
	case "register":
		if r.Method != http.MethodPost {
			writePublicError(w, http.StatusMethodNotAllowed, "POST required")
			return true
		}
		h.authRegister(w, r, siteID, readDB, writeDB, physTable, ae)
		return true
	case "login":
		if r.Method != http.MethodPost {
			writePublicError(w, http.StatusMethodNotAllowed, "POST required")
			return true
		}
		h.authLogin(w, r, siteID, readDB, physTable, ae)
		return true
	case "me":
		if r.Method != http.MethodGet {
			writePublicError(w, http.StatusMethodNotAllowed, "GET required")
			return true
		}
		h.authMe(w, r, siteID, readDB, physTable, ae)
		return true
	case "refresh":
		if r.Method != http.MethodPost {
			writePublicError(w, http.StatusMethodNotAllowed, "POST required")
			return true
		}
		h.authRefresh(w, r, siteID, readDB, physTable, ae)
		return true
	}

	// OAuth routes: oauth/{provider} or oauth/{provider}/callback
	if strings.HasPrefix(action, "oauth/") {
		oauthParts := strings.SplitN(action[6:], "/", 2) // strip "oauth/" prefix
		provider := oauthParts[0]
		if len(oauthParts) == 2 && oauthParts[1] == "callback" {
			h.oauthCallback(w, r, siteID, readDB, writeDB, physTable, ae, provider)
		} else {
			h.oauthAuthorize(w, r, readDB, ae, provider)
		}
		return true
	}

	return false
}

// authRegister handles POST /api/{path}/register.
// readDB is used for SELECT queries; writeDB is used only for the final INSERT.
// Password hashing happens before touching writeDB to avoid blocking the write pool.
func (h *Handler) authRegister(w http.ResponseWriter, r *http.Request, siteID int, readDB *sql.DB, writeDB *sql.DB, physTable string, ae *authEndpointConfig) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writePublicError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Validate required fields.
	username, ok := body[ae.UsernameColumn].(string)
	if !ok || username == "" {
		writePublicError(w, http.StatusBadRequest, fmt.Sprintf("%s is required", ae.UsernameColumn))
		return
	}
	password, ok := body[ae.PasswordColumn].(string)
	if !ok || password == "" {
		writePublicError(w, http.StatusBadRequest, fmt.Sprintf("%s is required", ae.PasswordColumn))
		return
	}

	// Check if user already exists (read pool — no write lock needed).
	var existingID int
	err := readDB.QueryRow(
		fmt.Sprintf("SELECT id FROM %s WHERE %s = ?", physTable, ae.UsernameColumn),
		username,
	).Scan(&existingID)
	if err == nil {
		writePublicError(w, http.StatusConflict, fmt.Sprintf("user with this %s already exists", ae.UsernameColumn))
		return
	}

	// Hash the password BEFORE touching the write pool — this is CPU-intensive
	// and must not block the single write connection.
	hashedPassword, err := security.HashPassword(password)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	body[ae.PasswordColumn] = hashedPassword

	// Force default role (prevent self-assignment of admin, etc.).
	body[ae.RoleColumn] = ae.DefaultRole

	// Remove id if provided.
	delete(body, "id")

	// Load actual table columns to filter out unknown fields from request.
	// This prevents INSERT failures when the frontend sends extra fields
	// like "confirm_password", "terms_accepted", etc.
	// Uses read pool — PRAGMA is a read operation.
	colRows, colErr := readDB.Query(fmt.Sprintf("PRAGMA table_info(%s)", physTable))
	if colErr == nil {
		validCols := make(map[string]bool)
		for colRows.Next() {
			var cid int
			var name, colType string
			var notNull int
			var dfltValue sql.NullString
			var pk int
			colRows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk)
			validCols[name] = true
		}
		colRows.Close()

		if len(validCols) == 0 {
			writePublicError(w, http.StatusInternalServerError,
				fmt.Sprintf("registration failed: table %q does not exist", ae.TableName))
			return
		}

		// Verify the auth config columns actually exist in the table.
		if !validCols[ae.UsernameColumn] || !validCols[ae.PasswordColumn] {
			writePublicError(w, http.StatusInternalServerError,
				fmt.Sprintf("auth config mismatch: table %q is missing column %q or %q",
					ae.TableName, ae.UsernameColumn, ae.PasswordColumn))
			return
		}

		// Filter unknown fields — only inside the validCols guard.
		for col := range body {
			if !validCols[col] {
				delete(body, col)
			}
		}
	}

	// Insert the row — validate column names from request body.
	columns := make([]string, 0, len(body))
	placeholders := make([]string, 0, len(body))
	values := make([]interface{}, 0, len(body))
	for col, val := range body {
		if err := security.ValidateColumnName(col); err != nil {
			writePublicError(w, http.StatusBadRequest, "invalid field name: "+col)
			return
		}
		columns = append(columns, col)
		placeholders = append(placeholders, "?")
		values = append(values, val)
	}

	if len(columns) == 0 {
		writePublicError(w, http.StatusInternalServerError,
			fmt.Sprintf("no valid columns for registration — table=%q, username_col=%q, password_col=%q, role_col=%q",
				ae.TableName, ae.UsernameColumn, ae.PasswordColumn, ae.RoleColumn))
		return
	}

	// Only the INSERT uses the write pool — all prior work used the read pool.
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", physTable, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	result, err := writeDB.Exec(query, values...)
	if err != nil {
		writePublicError(w, http.StatusBadRequest, "registration failed: "+err.Error())
		return
	}

	userID, _ := result.LastInsertId()

	// Generate JWT token.
	token, err := h.generateUserToken(int(userID), username, ae.DefaultRole, siteID, ae)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Publish auth.register event for server-side actions (email confirmation, etc.).
	if h.deps.Bus != nil {
		payload := map[string]interface{}{
			"user_id": userID,
			"table":   ae.TableName,
		}
		// Include non-sensitive fields from the registration body for action templates.
		for col, val := range body {
			if col != ae.PasswordColumn && col != "id" {
				payload[col] = val
			}
		}
		h.deps.Bus.Publish(events.NewEvent(events.EventAuthRegister, siteID, payload))
	}

	writePublicJSON(w, http.StatusCreated, map[string]interface{}{
		"success": true,
		"user_id": userID,
		"token":   token,
	})
}

// authLogin handles POST /api/{path}/login.
// Login is entirely read-only (SELECT + bcrypt compare) — no write pool needed.
func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request, siteID int, readDB *sql.DB, physTable string, ae *authEndpointConfig) {
	var body map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writePublicError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	username, ok := body[ae.UsernameColumn].(string)
	if !ok || username == "" {
		writePublicError(w, http.StatusBadRequest, fmt.Sprintf("%s is required", ae.UsernameColumn))
		return
	}
	password, ok := body[ae.PasswordColumn].(string)
	if !ok || password == "" {
		writePublicError(w, http.StatusBadRequest, fmt.Sprintf("%s is required", ae.PasswordColumn))
		return
	}

	// Find the user by username (include role column). Read pool — no writes.
	var userID int
	var hashedPassword string
	var role sql.NullString
	err := readDB.QueryRow(
		fmt.Sprintf("SELECT id, %s, %s FROM %s WHERE %s = ?", ae.PasswordColumn, ae.RoleColumn, physTable, ae.UsernameColumn),
		username,
	).Scan(&userID, &hashedPassword, &role)
	if err != nil && strings.Contains(err.Error(), "no such column") {
		// Role column doesn't exist — retry without it, use default role.
		err = readDB.QueryRow(
			fmt.Sprintf("SELECT id, %s FROM %s WHERE %s = ?", ae.PasswordColumn, physTable, ae.UsernameColumn),
			username,
		).Scan(&userID, &hashedPassword)
		role.String = ae.DefaultRole
	}
	if err != nil {
		writePublicError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// Verify password — CPU-intensive but doesn't touch the database.
	if !security.CheckPassword(password, hashedPassword) {
		writePublicError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	userRole := role.String
	if userRole == "" {
		userRole = ae.DefaultRole
	}

	// Generate JWT token.
	token, err := h.generateUserToken(userID, username, userRole, siteID, ae)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Publish auth.login event for server-side actions (update last_login, etc.).
	if h.deps.Bus != nil {
		h.deps.Bus.Publish(events.NewEvent(events.EventAuthLogin, siteID, map[string]interface{}{
			"user_id":         userID,
			"table":           ae.TableName,
			ae.UsernameColumn: username,
			"role":            userRole,
		}))
	}

	writePublicJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"user_id": userID,
		"token":   token,
	})
}

// authMe handles GET /api/{path}/me.
// Pure read operation — uses read pool only.
func (h *Handler) authMe(w http.ResponseWriter, r *http.Request, siteID int, readDB *sql.DB, physTable string, ae *authEndpointConfig) {
	// Extract and validate JWT token — must be scoped to this site.
	claims, err := h.extractUserClaims(r)
	if err != nil || claims.SiteID != siteID {
		writePublicError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Fetch user data (excluding password columns).
	cols := "id, " + ae.UsernameColumn
	if len(ae.PublicColumns) > 0 {
		cols = strings.Join(ae.PublicColumns, ", ")
	}

	query := fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", cols, physTable)
	rows, err := readDB.Query(query, claims.UserID)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "query error")
		return
	}
	defer rows.Close()

	results := h.scanRowsToMaps(rows)
	if len(results) == 0 {
		writePublicError(w, http.StatusNotFound, "user not found")
		return
	}

	writePublicJSON(w, http.StatusOK, results[0])
}

// authRefresh handles POST /api/{path}/refresh.
// Accepts a valid JWT and returns a new token with a fresh expiry.
// The user must still exist in the database; the token must be scoped to this site.
func (h *Handler) authRefresh(w http.ResponseWriter, r *http.Request, siteID int, readDB *sql.DB, physTable string, ae *authEndpointConfig) {
	claims, err := h.extractUserClaims(r)
	if err != nil || claims.SiteID != siteID {
		writePublicError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Verify the user still exists in the database.
	var exists int
	err = readDB.QueryRow(
		fmt.Sprintf("SELECT 1 FROM %s WHERE id = ?", physTable),
		claims.UserID,
	).Scan(&exists)
	if err != nil {
		writePublicError(w, http.StatusUnauthorized, "user no longer exists")
		return
	}

	// Fetch current role in case it changed since the token was issued.
	var role sql.NullString
	_ = readDB.QueryRow(
		fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", ae.RoleColumn, physTable),
		claims.UserID,
	).Scan(&role)
	userRole := role.String
	if userRole == "" {
		userRole = ae.DefaultRole
	}

	token, err := h.generateUserToken(claims.UserID, claims.Username, userRole, siteID, ae)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	writePublicJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"user_id": claims.UserID,
		"token":   token,
	})
}

// generateUserToken creates a JWT token for a site user, scoped to the site.
// The ae parameter is accepted (but not yet used) so callers can pass auth
// endpoint config for future per-endpoint token customization.
func (h *Handler) generateUserToken(userID int, username, role string, siteID int, _ *authEndpointConfig) (string, error) {
	if h.deps.JWTManager == nil {
		return "", fmt.Errorf("JWT manager not configured")
	}
	return h.deps.JWTManager.GenerateForSite(siteID, userID, username, role)
}

// extractUserClaims extracts and validates JWT claims from the Authorization header.
func (h *Handler) extractUserClaims(r *http.Request) (*security.Claims, error) {
	if h.deps.JWTManager == nil {
		return nil, fmt.Errorf("JWT manager not configured")
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, fmt.Errorf("no authorization header")
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		return nil, fmt.Errorf("invalid authorization format")
	}

	return h.deps.JWTManager.Validate(token)
}

// validateSiteTokenOrJWT checks if the token is either a valid site API key
// or a valid user JWT token scoped to this site. Returns true if either check passes.
func (h *Handler) validateSiteTokenOrJWT(siteID int, token string) bool {
	if token == "" {
		return false
	}

	// First try: site API key.
	if h.validateSiteToken(siteID, token) {
		return true
	}

	// Second try: user JWT — must be scoped to this site.
	if h.deps.JWTManager != nil {
		claims, err := h.deps.JWTManager.Validate(token)
		return err == nil && claims.SiteID == siteID
	}

	return false
}

// --- OAuth (Social Login) ---

type oauthProviderConfig struct {
	ID               int
	Name             string
	DisplayName      string
	ClientID         string
	ClientSecretName string
	AuthorizeURL     string
	TokenURL         string
	UserinfoURL      string
	Scopes           string
	UsernameField    string
	AuthEndpointPath string
}

func loadOAuthProvider(siteDB *sql.DB, providerName, authPath string) (*oauthProviderConfig, error) {
	var op oauthProviderConfig
	err := siteDB.QueryRow(
		"SELECT id, name, display_name, client_id, client_secret_name, authorize_url, token_url, userinfo_url, scopes, username_field, auth_endpoint_path FROM ho_oauth_providers WHERE name = ? AND auth_endpoint_path = ? AND is_enabled = 1",
		providerName, authPath,
	).Scan(&op.ID, &op.Name, &op.DisplayName, &op.ClientID, &op.ClientSecretName, &op.AuthorizeURL, &op.TokenURL, &op.UserinfoURL, &op.Scopes, &op.UsernameField, &op.AuthEndpointPath)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

// oauthAuthorize handles GET /api/{authPath}/oauth/{provider} — redirects to the OAuth provider.
func (h *Handler) oauthAuthorize(w http.ResponseWriter, r *http.Request, readDB *sql.DB, ae *authEndpointConfig, provider string) {
	op, err := loadOAuthProvider(readDB, provider, ae.Path)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "OAuth provider not configured")
		return
	}

	// Generate HMAC-signed state for CSRF protection.
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to generate state")
		return
	}
	stateRandom := hex.EncodeToString(randomBytes)
	stateMAC := hex.EncodeToString(h.deps.JWTManager.HMAC(randomBytes))
	state := stateRandom + "." + stateMAC

	// Store state in a short-lived cookie (SameSite=Lax for cross-site redirect back).
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/api/" + ae.Path + "/oauth/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})

	redirectURI := oauthRedirectURI(r, ae.Path, provider)

	// Build authorization URL.
	authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&state=%s",
		op.AuthorizeURL,
		url.QueryEscape(op.ClientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(op.Scopes),
		url.QueryEscape(state),
	)

	http.Redirect(w, r, authURL, http.StatusFound)
}

// oauthCallback handles GET /api/{authPath}/oauth/{provider}/callback — exchanges code for token.
func (h *Handler) oauthCallback(w http.ResponseWriter, r *http.Request, siteID int, readDB *sql.DB, writeDB *sql.DB, physTable string, ae *authEndpointConfig, provider string) {
	// Validate state parameter.
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil {
		writePublicError(w, http.StatusBadRequest, "missing OAuth state cookie")
		return
	}
	queryState := r.URL.Query().Get("state")
	if queryState == "" || queryState != stateCookie.Value {
		writePublicError(w, http.StatusBadRequest, "invalid OAuth state")
		return
	}
	// Verify HMAC on state.
	parts := strings.SplitN(queryState, ".", 2)
	if len(parts) != 2 {
		writePublicError(w, http.StatusBadRequest, "malformed OAuth state")
		return
	}
	randomBytes, err := hex.DecodeString(parts[0])
	if err != nil {
		writePublicError(w, http.StatusBadRequest, "invalid OAuth state encoding")
		return
	}
	expectedMAC := hex.EncodeToString(h.deps.JWTManager.HMAC(randomBytes))
	if !hmac.Equal([]byte(parts[1]), []byte(expectedMAC)) {
		writePublicError(w, http.StatusBadRequest, "OAuth state verification failed")
		return
	}
	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/api/" + ae.Path + "/oauth/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	// Check for error from provider.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		writePublicError(w, http.StatusBadRequest, "OAuth error: "+errParam)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		writePublicError(w, http.StatusBadRequest, "missing authorization code")
		return
	}

	// Load OAuth provider config (read pool).
	op, err := loadOAuthProvider(readDB, provider, ae.Path)
	if err != nil {
		writePublicError(w, http.StatusNotFound, "OAuth provider not configured")
		return
	}

	// Decrypt client secret (read pool).
	var encryptedSecret string
	err = readDB.QueryRow("SELECT value_encrypted FROM ho_secrets WHERE name = ?", op.ClientSecretName).Scan(&encryptedSecret)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "OAuth client secret not found")
		return
	}
	clientSecret, err := h.deps.Encryptor.Decrypt(encryptedSecret)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to decrypt OAuth secret")
		return
	}

	redirectURI := oauthRedirectURI(r, ae.Path, provider)

	// Exchange authorization code for access token.
	tokenResp, err := exchangeOAuthCode(op, code, redirectURI, clientSecret)
	if err != nil {
		writePublicError(w, http.StatusBadGateway, "failed to exchange OAuth code: "+err.Error())
		return
	}

	// Fetch user info from provider.
	userInfo, err := fetchOAuthUserInfo(op, tokenResp.AccessToken)
	if err != nil {
		writePublicError(w, http.StatusBadGateway, "failed to fetch user info: "+err.Error())
		return
	}

	// Extract username from the configured field.
	username, ok := userInfo[op.UsernameField].(string)
	if !ok || username == "" {
		writePublicError(w, http.StatusBadGateway, fmt.Sprintf("user info missing %s field", op.UsernameField))
		return
	}

	// Find or create user in the site's user table.
	var userID int
	var role sql.NullString
	err = readDB.QueryRow(
		fmt.Sprintf("SELECT id, %s FROM %s WHERE %s = ?", ae.RoleColumn, physTable, ae.UsernameColumn),
		username,
	).Scan(&userID, &role)
	if err == sql.ErrNoRows {
		// Create new user with an unusable password and default role.
		// Hash before write pool — CPU-intensive.
		unusableHash, _ := security.HashPassword(hex.EncodeToString(randomBytes) + "!oauth-no-password-login")
		result, err := writeDB.Exec(
			fmt.Sprintf("INSERT INTO %s (%s, %s, %s) VALUES (?, ?, ?)", physTable, ae.UsernameColumn, ae.PasswordColumn, ae.RoleColumn),
			username, unusableHash, ae.DefaultRole,
		)
		if err != nil {
			writePublicError(w, http.StatusInternalServerError, "failed to create user")
			return
		}
		id, _ := result.LastInsertId()
		userID = int(id)
		role.String = ae.DefaultRole
	} else if err != nil {
		writePublicError(w, http.StatusInternalServerError, "database error")
		return
	}

	userRole := role.String
	if userRole == "" {
		userRole = ae.DefaultRole
	}

	// Generate JWT.
	jwtToken, err := h.generateUserToken(userID, username, userRole, siteID, ae)
	if err != nil {
		writePublicError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Redirect to frontend with token in URL fragment (not query param).
	// Fragments are never sent to the server, preventing token leakage
	// in server logs, Referer headers, and browser history.
	http.Redirect(w, r, "/#token="+url.QueryEscape(jwtToken), http.StatusFound)
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// oauthRedirectURI builds the OAuth callback URI, using the site's configured
// domain to prevent open-redirect attacks via Host header manipulation.
// Falls back to r.Host only when no domain is configured (local development).
func oauthRedirectURI(r *http.Request, authPath, provider string) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	if site := getSite(r); site != nil && site.Domain != nil && *site.Domain != "" {
		host = *site.Domain
	}
	return fmt.Sprintf("%s://%s/api/%s/oauth/%s/callback", scheme, host, authPath, provider)
}

func exchangeOAuthCode(op *oauthProviderConfig, code, redirectURI, clientSecret string) (*oauthTokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {op.ClientID},
		"client_secret": {clientSecret},
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodPost, op.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp oauthTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		// Some providers (GitHub) return form-encoded responses.
		vals, _ := url.ParseQuery(string(body))
		tokenResp.AccessToken = vals.Get("access_token")
		tokenResp.TokenType = vals.Get("token_type")
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("no access_token in response")
	}

	return &tokenResp, nil
}

func fetchOAuthUserInfo(op *oauthProviderConfig, accessToken string) (map[string]interface{}, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, op.UserinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo request failed (status %d)", resp.StatusCode)
	}

	var info map[string]interface{}
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("invalid userinfo JSON: %w", err)
	}

	return info, nil
}
