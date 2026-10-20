package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
)

// setupTestServer creates an isolated *Server backed by sqlmock.
func setupTestServer(t *testing.T) (*Server, sqlmock.Sqlmock) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
	})
	srv := &Server{
		db:  db,
		rdb: nil,
	}
	return srv, mock
}

// =========================================================================
// 1. Bcrypt Password Hashing & Verification Tests (Tests 1-4)
// =========================================================================

// Test 1: Asserts that HashPassword produces a non-empty string starting with "$2a$" or "$2b$" and no error.
func TestHashPassword_Success(t *testing.T) {
	password := "securePassword123"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("unexpected error hashing password: %v", err)
	}
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if !strings.HasPrefix(hash, "$2a$") && !strings.HasPrefix(hash, "$2b$") {
		t.Errorf("expected bcrypt hash prefix ($2a$ or $2b$), got: %s", hash[:4])
	}
}

// Test 2: Asserts that hashing the same password twice yields two distinct hashes due to random salting.
func TestHashPassword_UniqueSalts(t *testing.T) {
	password := "samePasswordToHash"
	hash1, err1 := HashPassword(password)
	if err1 != nil {
		t.Fatalf("unexpected error on hash 1: %v", err1)
	}
	hash2, err2 := HashPassword(password)
	if err2 != nil {
		t.Fatalf("unexpected error on hash 2: %v", err2)
	}
	if hash1 == hash2 {
		t.Errorf("expected unique salts producing different hashes, but hashes are identical")
	}
}

// Test 3: Table-driven test verifying matching, wrong, and case-sensitive password verification.
func TestCheckPasswordHash_SuccessAndMismatch(t *testing.T) {
	password := "CorrectPassword123!"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("failed to create hash: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"exact match", "CorrectPassword123!", true},
		{"wrong password", "WrongPassword456", false},
		{"case sensitive difference", "correctpassword123!", false},
		{"empty password", "", false},
		{"trailing space", "CorrectPassword123! ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			match := CheckPasswordHash(tc.input, hash)
			if match != tc.expected {
				t.Errorf("expected match=%v for input %q, got %v", tc.expected, tc.input, match)
			}
		})
	}
}

// Test 4: Verifies that invalid hashes (empty, corrupted, truncated) return false without panic.
func TestCheckPasswordHash_MalformedOrEmpty(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{"empty hash", ""},
		{"arbitrary text", "not-a-valid-bcrypt-hash"},
		{"truncated bcrypt hash", "$2a$10$abcdef"},
		{"malformed cost", "$2a$99$invalidcostcharactershere123456789012345678901234567890"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			match := CheckPasswordHash("anyPassword", tc.hash)
			if match {
				t.Errorf("expected CheckPasswordHash to return false for malformed hash %q", tc.hash)
			}
		})
	}
}

// =========================================================================
// 2. JWT Generation & Validation Tests (Tests 5-10)
// =========================================================================

// Test 5: Asserts that GenerateToken produces a valid 3-part compact JWT string.
func TestGenerateToken_Success(t *testing.T) {
	userID := "a0000000-0000-0000-0000-000000000001"
	email := "user@example.com"

	token, err := GenerateToken(userID, email)
	if err != nil {
		t.Fatalf("unexpected error generating token: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token string")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected compact 3-part JWT, got %d parts", len(parts))
	}
}

// Test 6: Generates token and validates it; verifies UserID and Email claims match.
func TestValidateToken_Valid(t *testing.T) {
	userID := "a0000000-0000-0000-0000-000000000002"
	email := "validate@example.com"

	token, err := GenerateToken(userID, email)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	claims, err := ValidateToken(token)
	if err != nil {
		t.Fatalf("expected valid token, got error: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("expected UserID %q, got %q", userID, claims.UserID)
	}
	if claims.Email != email {
		t.Errorf("expected Email %q, got %q", email, claims.Email)
	}
}

// Test 7: Generates a token with an expiration in the past; asserts ValidateToken returns error.
func TestValidateToken_Expired(t *testing.T) {
	userID := "a0000000-0000-0000-0000-000000000003"
	email := "expired@example.com"

	token, err := GenerateTokenWithDuration(userID, email, -1*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate expired token: %v", err)
	}

	_, err = ValidateToken(token)
	if err == nil {
		t.Fatal("expected error validating expired token, got nil")
	}
}

// Test 8: Generates a token signed with Secret A, attempts validation with Secret B; asserts error.
func TestValidateToken_InvalidSignature(t *testing.T) {
	now := time.Now()
	claims := JWTClaims{
		UserID: "a0000000-0000-0000-0000-000000000004",
		Email:  "tampered@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "a0000000-0000-0000-0000-000000000004",
			Issuer:    "fintech-ledger-engine",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	foreignToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := foreignToken.SignedString([]byte("completely-different-signing-secret-key-12345"))
	if err != nil {
		t.Fatalf("failed to create foreign-signed token: %v", err)
	}

	_, err = ValidateToken(tokenStr)
	if err == nil {
		t.Fatal("expected signature verification failure for token signed with different key")
	}
}

// Test 9: Modifies a byte in the payload segment; asserts signature verification failure.
func TestValidateToken_TamperedPayload(t *testing.T) {
	token, err := GenerateToken("a0000000-0000-0000-0000-000000000005", "test@tamper.com")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("invalid token format: %s", token)
	}

	// Tamper with payload
	payloadBytes := []byte(parts[1])
	if len(payloadBytes) > 5 {
		if payloadBytes[5] == 'A' {
			payloadBytes[5] = 'B'
		} else {
			payloadBytes[5] = 'A'
		}
	}
	tamperedToken := parts[0] + "." + string(payloadBytes) + "." + parts[2]

	_, err = ValidateToken(tamperedToken)
	if err == nil {
		t.Fatal("expected ValidateToken to fail for tampered payload, but succeeded")
	}
}

// Test 10: Table-driven test with empty string, single segment, two segments, invalid base64.
func TestValidateToken_Malformed(t *testing.T) {
	malformed := []string{
		"",
		"   ",
		"single-segment-token",
		"header.payload",
		"header.payload.signature.extra",
		"Bearer abc.def.ghi",
		"notbase64!.notbase64@.notbase64#",
	}

	for _, tokenStr := range malformed {
		_, err := ValidateToken(tokenStr)
		if err == nil {
			t.Errorf("expected error for malformed token %q, got nil", tokenStr)
		}
	}
}

// =========================================================================
// 3. HTTP Register Endpoint Tests (Tests 11-14)
// =========================================================================

// Test 11: Registers new user, asserts 201 Created, non-empty token, and no leaked password_hash.
func TestHandleRegister_Success_201(t *testing.T) {
	srv, mock := setupTestServer(t)

	expectedID := "a0000000-0000-0000-0000-000000000001"
	expectedEmail := "user@example.com"
	expectedName := "Eduardo Perez"

	// 1. Duplicate check query (returns no rows)
	mock.ExpectQuery(`SELECT .+ FROM users WHERE LOWER\(email\) = \$1`).
		WithArgs(expectedEmail).
		WillReturnError(sql.ErrNoRows)

	// 2. User insertion query
	mock.ExpectQuery(`INSERT INTO users`).
		WithArgs(expectedEmail, sqlmock.AnyArg(), expectedName).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password_hash", "full_name", "created_at", "updated_at"}).
			AddRow(expectedID, expectedEmail, "$2a$10$hashedPasswordPlaceholder", expectedName, time.Now(), time.Now()))

	body := `{"email":"user@example.com","password":"securePassword123","full_name":"Eduardo Perez"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleRegister(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d. Body: %s", resp.StatusCode, w.Body.String())
	}

	var res AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}

	if res.User.ID != expectedID {
		t.Errorf("expected user.id %q, got %q", expectedID, res.User.ID)
	}
	if res.User.Email != expectedEmail {
		t.Errorf("expected user.email %q, got %q", expectedEmail, res.User.Email)
	}
	if res.User.FullName != expectedName {
		t.Errorf("expected user.full_name %q, got %q", expectedName, res.User.FullName)
	}
	if res.Token == "" {
		t.Error("expected non-empty token in response")
	}

	// Verify password_hash is strictly NOT leaked in response body
	if strings.Contains(w.Body.String(), "password_hash") {
		t.Error("security violation: password_hash was leaked in register response")
	}

	// Verify HttpOnly cookie was set
	cookies := resp.Cookies()
	var authCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == AuthCookieName {
			authCookie = c
			break
		}
	}
	if authCookie == nil {
		t.Error("expected auth cookie to be set on register")
	} else if !authCookie.HttpOnly {
		t.Error("expected auth cookie to have HttpOnly=true")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled sql expectations: %v", err)
	}
}

// Test 12: Rejects duplicate email registration with 400 Bad Request.
func TestHandleRegister_DuplicateEmail_400(t *testing.T) {
	srv, mock := setupTestServer(t)

	existingEmail := "duplicate@example.com"

	// Duplicate check returns existing record
	mock.ExpectQuery(`SELECT .+ FROM users WHERE LOWER\(email\) = \$1`).
		WithArgs(existingEmail).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password_hash", "full_name", "created_at", "updated_at"}).
			AddRow("a0000000-0000-0000-0000-000000000009", existingEmail, "hash", "Existing User", time.Now(), time.Now()))

	body := `{"email":"duplicate@example.com","password":"securePassword123","full_name":"Second User"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleRegister(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400 Bad Request, got %d", resp.StatusCode)
	}

	if !strings.Contains(w.Body.String(), "email already registered") {
		t.Errorf("expected 'email already registered' in body, got: %s", w.Body.String())
	}
}

// Test 13: Table-driven input validation testing (broken JSON, missing fields, short password).
func TestHandleRegister_InvalidInput_400(t *testing.T) {
	srv, _ := setupTestServer(t)

	tests := []struct {
		name string
		body string
	}{
		{"broken JSON syntax", `{"email": "broken`},
		{"empty body", `{}`},
		{"missing email", `{"password":"securePassword123","full_name":"Test User"}`},
		{"invalid email format", `{"email":"notanemail","password":"securePassword123","full_name":"Test User"}`},
		{"short password (< 6 chars)", `{"email":"user@example.com","password":"123","full_name":"Test User"}`},
		{"missing full_name", `{"email":"user@example.com","password":"securePassword123","full_name":""}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			srv.handleRegister(w, req)

			resp := w.Result()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected status 400 Bad Request, got %d", resp.StatusCode)
			}
		})
	}
}

// Test 14: Method not allowed on register (GET).
func TestHandleRegister_MethodNotAllowed_405(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/register", nil)
	w := httptest.NewRecorder()

	srv.handleRegister(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 Method Not Allowed, got %d", resp.StatusCode)
	}
}

// =========================================================================
// 4. HTTP Login Endpoint Tests (Tests 15-19)
// =========================================================================

// Test 15: Validates credentials, returns 200 OK, valid token, and HttpOnly cookie.
func TestHandleLogin_Success_200(t *testing.T) {
	srv, mock := setupTestServer(t)

	email := "login@example.com"
	rawPassword := "securePassword123"
	hashedPassword, err := HashPassword(rawPassword)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	mock.ExpectQuery(`SELECT .+ FROM users WHERE LOWER\(email\) = \$1`).
		WithArgs(email).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password_hash", "full_name", "created_at", "updated_at"}).
			AddRow("a0000000-0000-0000-0000-000000000001", email, hashedPassword, "Eduardo Perez", time.Now(), time.Now()))

	body := `{"email":"login@example.com","password":"securePassword123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d. Body: %s", resp.StatusCode, w.Body.String())
	}

	var res AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode login response: %v", err)
	}
	if res.Token == "" {
		t.Error("expected non-empty token")
	}
	if res.User.Email != email {
		t.Errorf("expected email %q, got %q", email, res.User.Email)
	}

	// Verify cookie
	cookies := resp.Cookies()
	var authCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == AuthCookieName {
			authCookie = c
			break
		}
	}
	if authCookie == nil {
		t.Fatal("expected HttpOnly auth cookie to be set")
	}
	if !authCookie.HttpOnly {
		t.Error("expected auth cookie to have HttpOnly=true")
	}
	if authCookie.Path != "/" {
		t.Errorf("expected cookie path '/', got %q", authCookie.Path)
	}
}

// Test 16: Rejects wrong password with 401 Unauthorized and generic error.
func TestHandleLogin_WrongPassword_401(t *testing.T) {
	srv, mock := setupTestServer(t)

	email := "user@example.com"
	correctHash, _ := HashPassword("correctPassword123")

	mock.ExpectQuery(`SELECT .+ FROM users WHERE LOWER\(email\) = \$1`).
		WithArgs(email).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password_hash", "full_name", "created_at", "updated_at"}).
			AddRow("a0000000-0000-0000-0000-000000000001", email, correctHash, "Eduardo Perez", time.Now(), time.Now()))

	body := `{"email":"user@example.com","password":"wrongPassword123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", resp.StatusCode)
	}

	if !strings.Contains(w.Body.String(), "invalid credentials") {
		t.Errorf("expected 'invalid credentials' in error response, got: %s", w.Body.String())
	}

	// Verify NO auth cookie is issued on failed login
	cookies := resp.Cookies()
	for _, c := range cookies {
		if c.Name == AuthCookieName && c.Value != "" && c.MaxAge > 0 {
			t.Errorf("cookie %s should not be set on failed login", c.Name)
		}
	}
}

// Test 17: Rejects non-existent email with uniform 401 Unauthorized (preventing user enumeration).
func TestHandleLogin_UnknownEmail_401(t *testing.T) {
	srv, mock := setupTestServer(t)

	unknownEmail := "doesnotexist@example.com"
	mock.ExpectQuery(`SELECT .+ FROM users WHERE LOWER\(email\) = \$1`).
		WithArgs(unknownEmail).
		WillReturnError(sql.ErrNoRows)

	body := `{"email":"doesnotexist@example.com","password":"anyPassword123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", resp.StatusCode)
	}

	if !strings.Contains(w.Body.String(), "invalid credentials") {
		t.Errorf("expected 'invalid credentials' error, got: %s", w.Body.String())
	}
}

// Test 18: Table-driven input validation for login (broken JSON, missing email/password).
func TestHandleLogin_InvalidInput_400(t *testing.T) {
	srv, _ := setupTestServer(t)

	tests := []struct {
		name string
		body string
	}{
		{"broken JSON", `{"email": "broken`},
		{"empty JSON object", `{}`},
		{"missing email", `{"password":"somePassword"}`},
		{"missing password", `{"email":"user@example.com"}`},
		{"empty email string", `{"email":"","password":"somePassword"}`},
		{"empty password string", `{"email":"user@example.com","password":""}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			srv.handleLogin(w, req)

			resp := w.Result()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected status 400 Bad Request, got %d", resp.StatusCode)
			}
		})
	}
}

// Test 19: Method not allowed on login (GET).
func TestHandleLogin_MethodNotAllowed_405(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	w := httptest.NewRecorder()

	srv.handleLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 Method Not Allowed, got %d", resp.StatusCode)
	}
}

// =========================================================================
// 5. HTTP Me Endpoint Tests (Tests 20-22)
// =========================================================================

// Test 20: Returns user profile with valid Bearer token.
func TestHandleMe_Success_BearerToken_200(t *testing.T) {
	srv, mock := setupTestServer(t)

	userID := "a0000000-0000-0000-0000-000000000001"
	email := "me@example.com"
	fullName := "Eduardo Perez"

	token, err := GenerateToken(userID, email)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	mock.ExpectQuery(`SELECT .+ FROM users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password_hash", "full_name", "created_at", "updated_at"}).
			AddRow(userID, email, "hash", fullName, time.Now(), time.Now()))

	handler := srv.AuthMiddleware(srv.handleMe)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d. Body: %s", resp.StatusCode, w.Body.String())
	}

	var res MeResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode me response: %v", err)
	}
	if res.User.ID != userID || res.User.Email != email || res.User.FullName != fullName {
		t.Errorf("user profile mismatch: got %+v", res.User)
	}
}

// Test 21: Returns user profile with valid Cookie token.
func TestHandleMe_Success_CookieToken_200(t *testing.T) {
	srv, mock := setupTestServer(t)

	userID := "a0000000-0000-0000-0000-000000000002"
	email := "cookie@example.com"
	fullName := "Cookie User"

	token, err := GenerateToken(userID, email)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	mock.ExpectQuery(`SELECT .+ FROM users WHERE id = \$1`).
		WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "password_hash", "full_name", "created_at", "updated_at"}).
			AddRow(userID, email, "hash", fullName, time.Now(), time.Now()))

	handler := srv.AuthMiddleware(srv.handleMe)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: AuthCookieName, Value: token})
	w := httptest.NewRecorder()

	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d. Body: %s", resp.StatusCode, w.Body.String())
	}

	var res MeResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode me response: %v", err)
	}
	if res.User.ID != userID || res.User.Email != email {
		t.Errorf("user profile mismatch: got %+v", res.User)
	}
}

// Test 22: Rejects unauthenticated request with 401 Unauthorized.
func TestHandleMe_Unauthenticated_401(t *testing.T) {
	srv, _ := setupTestServer(t)

	handler := srv.AuthMiddleware(srv.handleMe)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized, got %d", resp.StatusCode)
	}

	if !strings.Contains(w.Body.String(), "unauthorized") {
		t.Errorf("expected 'unauthorized' in response body, got: %s", w.Body.String())
	}
}

// =========================================================================
// 6. AuthMiddleware & Route Protection Tests (Tests 23-24)
// =========================================================================

// Test 23: Tests token extraction, context injection, and edge-case rejections in AuthMiddleware.
func TestAuthMiddleware_TokenExtractionAndContext(t *testing.T) {
	srv, _ := setupTestServer(t)

	userID := "a0000000-0000-0000-0000-000000000010"
	email := "middleware@example.com"
	validToken, err := GenerateToken(userID, email)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	expiredToken, err := GenerateTokenWithDuration(userID, email, -1*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate expired token: %v", err)
	}

	t.Run("Valid Bearer Header passes and injects context", func(t *testing.T) {
		var extractedUserID, extractedEmail string
		dummyHandler := func(w http.ResponseWriter, r *http.Request) {
			extractedUserID, _ = GetUserIDFromContext(r.Context())
			extractedEmail, _ = GetEmailFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}

		handler := srv.AuthMiddleware(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", w.Code)
		}
		if extractedUserID != userID {
			t.Errorf("expected context user_id %q, got %q", userID, extractedUserID)
		}
		if extractedEmail != email {
			t.Errorf("expected context email %q, got %q", email, extractedEmail)
		}
	})

	t.Run("Valid Cookie passes and injects context", func(t *testing.T) {
		var extractedUserID string
		dummyHandler := func(w http.ResponseWriter, r *http.Request) {
			extractedUserID, _ = GetUserIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}

		handler := srv.AuthMiddleware(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		req.AddCookie(&http.Cookie{Name: AuthCookieName, Value: validToken})
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 OK, got %d", w.Code)
		}
		if extractedUserID != userID {
			t.Errorf("expected context user_id %q, got %q", userID, extractedUserID)
		}
	})

	t.Run("Expired token rejected with 401", func(t *testing.T) {
		called := false
		dummyHandler := func(w http.ResponseWriter, r *http.Request) {
			called = true
		}

		handler := srv.AuthMiddleware(dummyHandler)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		req.Header.Set("Authorization", "Bearer "+expiredToken)
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
		if called {
			t.Error("inner handler should not be called on expired token")
		}
	})

	t.Run("Malformed Authorization header (not Bearer) rejected with 401", func(t *testing.T) {
		handler := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		req.Header.Set("Authorization", "Token "+validToken)
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("Empty Bearer token rejected with 401", func(t *testing.T) {
		handler := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
		req.Header.Set("Authorization", "Bearer ")
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", w.Code)
		}
	})

	t.Run("OPTIONS preflight returns 204 No Content without auth check", func(t *testing.T) {
		handler := srv.AuthMiddleware(func(w http.ResponseWriter, r *http.Request) {})
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/accounts", nil)
		w := httptest.NewRecorder()

		handler(w, req)

		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204 No Content, got %d", w.Code)
		}
	})
}

// Test 24: Verifies that unauthenticated requests to all protected routes return 401 Unauthorized.
func TestAuthMiddleware_ProtectedRoutesRejection(t *testing.T) {
	srv, _ := setupTestServer(t)

	routes := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"GET accounts", http.MethodGet, "/api/v1/accounts", srv.AuthMiddleware(srv.handleAccounts)},
		{"POST accounts", http.MethodPost, "/api/v1/accounts", srv.AuthMiddleware(srv.handleAccounts)},
		{"POST transfers", http.MethodPost, "/api/v1/transfers", srv.AuthMiddleware(srv.handleTransfer)},
		{"GET transactions", http.MethodGet, "/api/v1/transactions", srv.AuthMiddleware(srv.handleTransactions)},
		{"GET audit", http.MethodGet, "/api/v1/audit", srv.AuthMiddleware(srv.handleAudit)},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			w := httptest.NewRecorder()

			route.handler(w, req)

			resp := w.Result()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected route %s to return 401 Unauthorized, got %d", route.name, resp.StatusCode)
			}

			if !strings.Contains(w.Body.String(), "unauthorized") {
				t.Errorf("expected 'unauthorized' in body for route %s, got: %s", route.name, w.Body.String())
			}
		})
	}
}
