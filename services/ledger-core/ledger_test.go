package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	testUserID   = "11111111-2222-3333-4444-555555555555"
	testFundsID  = "aaaaaaaa-0000-0000-0000-000000000001"
	testTargetID = "bbbbbbbb-0000-0000-0000-000000000002"
	testOtherID  = "cccccccc-0000-0000-0000-000000000003"
)

var (
	reCount      = regexp.QuoteMeta(`SELECT COUNT(*) FROM accounts WHERE user_id = $1`)
	reInsertAcc  = regexp.QuoteMeta(`INSERT INTO accounts (account_number, name, type, currency, balance_cents, user_id)`)
	reIdem       = regexp.QuoteMeta(`SELECT id FROM transactions WHERE reference_id = $1`)
	reLockDep    = regexp.QuoteMeta(`SELECT id, type FROM accounts`)
	reLockTr     = regexp.QuoteMeta(`SELECT id, balance_cents, name, type FROM accounts`)
	reInsertTx   = regexp.QuoteMeta(`INSERT INTO transactions (reference_id, description, status, user_id)`)
	reInsertLine = regexp.QuoteMeta(`INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_cents)`)
	reDebitBal   = regexp.QuoteMeta(`UPDATE accounts SET balance_cents = balance_cents - $1`)
	reCreditBal  = regexp.QuoteMeta(`UPDATE accounts SET balance_cents = balance_cents + $1`)
)

func authedRequest(method, path, body, userID string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req = req.WithContext(context.WithValue(req.Context(), ContextKeyUserID, userID))
	}
	return req
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %q", w.Body.String())
	}
	return out
}

func mustMeetExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// =========================================================================
// Account provisioning
// =========================================================================

func TestAccountNumberFor_DeterministicAndUnique(t *testing.T) {
	a := accountNumberFor(testUserID, "FAL")
	if a != accountNumberFor(testUserID, "FAL") {
		t.Fatal("account number must be deterministic for the same user/code")
	}
	seen := map[string]bool{}
	for _, spec := range personalAccounts {
		n := accountNumberFor(testUserID, spec.Code)
		if seen[n] {
			t.Fatalf("duplicate account number %q", n)
		}
		seen[n] = true
		if len(n) > 64 {
			t.Fatalf("account number %q exceeds VARCHAR(64)", n)
		}
	}
	if accountNumberFor(testOtherID, "FAL") == a {
		t.Fatal("different users must get different account numbers")
	}
}

func TestPersonalAccounts_Catalog(t *testing.T) {
	want := map[string]string{
		"Banco Falabella": "ASSET", "Banco Estado": "ASSET", "Efectivo": "ASSET",
		"Ahorros": "ASSET", "Capital / Fondos Propios": externalFundsType,
	}
	if len(personalAccounts) != len(want) {
		t.Fatalf("expected %d personal accounts, got %d", len(want), len(personalAccounts))
	}
	equity := 0
	for _, s := range personalAccounts {
		if want[s.Name] != s.Type {
			t.Errorf("account %q: expected type %q, got %q", s.Name, want[s.Name], s.Type)
		}
		if s.Type == externalFundsType {
			equity++
		}
	}
	if equity != 1 {
		t.Fatalf("exactly one external-funds account is required, got %d", equity)
	}
}

func TestEnsurePersonalAccounts_CreatesAllWhenEmpty(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.ExpectQuery(reCount).WithArgs(testUserID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	for _, spec := range personalAccounts {
		mock.ExpectExec(reInsertAcc).
			WithArgs(accountNumberFor(testUserID, spec.Code), spec.Name, spec.Type, testUserID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	if err := ensurePersonalAccounts(srv.db, testUserID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustMeetExpectations(t, mock)
}

func TestEnsurePersonalAccounts_IdempotentWhenExisting(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.ExpectQuery(reCount).WithArgs(testUserID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	if err := ensurePersonalAccounts(srv.db, testUserID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mustMeetExpectations(t, mock) // no INSERT expected
}

// =========================================================================
// Deposits
// =========================================================================

func TestHandleDeposit_MethodNotAllowed(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodGet, "/api/v1/deposits", "", testUserID))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleDeposit_Unauthorized(t *testing.T) {
	srv, mock := setupTestServer(t)
	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		`{"idempotency_key":"k","account_id":"x","amount_cents":100}`, ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	mustMeetExpectations(t, mock)
}

func TestHandleDeposit_InvalidInput(t *testing.T) {
	cases := map[string]string{
		"zero amount":     `{"idempotency_key":"k","account_id":"a","amount_cents":0}`,
		"negative amount": `{"idempotency_key":"k","account_id":"a","amount_cents":-500}`,
		"missing account": `{"idempotency_key":"k","amount_cents":100}`,
		"malformed json":  `{"account_id":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv, mock := setupTestServer(t)
			w := httptest.NewRecorder()
			srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits", body, testUserID))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
			}
			mustMeetExpectations(t, mock) // must not touch the DB
		})
	}
}

// expectDepositPrelude mocks provisioning check, BEGIN, idempotency miss and the lock query.
func expectDepositPrelude(mock sqlmock.Sqlmock, lockRows *sqlmock.Rows) {
	mock.ExpectQuery(reCount).WithArgs(testUserID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectBegin()
	mock.ExpectQuery(reIdem).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(reLockDep).WithArgs(testUserID, testTargetID, externalFundsType).WillReturnRows(lockRows)
}

func TestHandleDeposit_Success_DoubleEntryBalanced(t *testing.T) {
	srv, mock := setupTestServer(t)
	const amount = int64(2500000)

	expectDepositPrelude(mock, sqlmock.NewRows([]string{"id", "type"}).
		AddRow(testFundsID, "EQUITY").AddRow(testTargetID, "ASSET"))
	mock.ExpectQuery(reInsertTx).
		WithArgs(txReference(testUserID, "dep-1"), "Sueldo", testUserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tx-1"))
	// Invariant: one DEBIT and one CREDIT of the same amount.
	mock.ExpectExec(reInsertLine).WithArgs("tx-1", testFundsID, "DEBIT", amount).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reInsertLine).WithArgs("tx-1", testTargetID, "CREDIT", amount).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reDebitBal).WithArgs(amount, testFundsID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reCreditBal).WithArgs(amount, testTargetID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		`{"idempotency_key":"dep-1","account_id":"`+testTargetID+`","amount_cents":2500000,"description":"Sueldo"}`, testUserID))

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	body := decodeBody(t, w)
	if body["transaction_id"] != "tx-1" || body["amount_cents"].(float64) != float64(amount) {
		t.Fatalf("unexpected body: %v", body)
	}
	mustMeetExpectations(t, mock)
}

func TestHandleDeposit_DefaultDescription(t *testing.T) {
	srv, mock := setupTestServer(t)
	expectDepositPrelude(mock, sqlmock.NewRows([]string{"id", "type"}).
		AddRow(testFundsID, "EQUITY").AddRow(testTargetID, "ASSET"))
	mock.ExpectQuery(reInsertTx).WithArgs(sqlmock.AnyArg(), "Ingreso de dinero", testUserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tx-2"))
	mock.ExpectExec(reInsertLine).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reInsertLine).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reDebitBal).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reCreditBal).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		`{"idempotency_key":"dep-2","account_id":"`+testTargetID+`","amount_cents":100}`, testUserID))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	mustMeetExpectations(t, mock)
}

func TestHandleDeposit_IdempotentReplay(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.ExpectQuery(reCount).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectBegin()
	mock.ExpectQuery(reIdem).WithArgs(txReference(testUserID, "dep-1")).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tx-existing"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		`{"idempotency_key":"dep-1","account_id":"`+testTargetID+`","amount_cents":100}`, testUserID))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on replay, got %d", w.Code)
	}
	body := decodeBody(t, w)
	if body["is_duplicate"] != true || body["transaction_id"] != "tx-existing" {
		t.Fatalf("expected duplicate marker, got %v", body)
	}
	mustMeetExpectations(t, mock) // no inserts on replay
}

func TestHandleDeposit_AccountOfAnotherUser_404(t *testing.T) {
	srv, mock := setupTestServer(t)
	// Lock query is scoped by user_id: the foreign account is simply not returned.
	expectDepositPrelude(mock, sqlmock.NewRows([]string{"id", "type"}).AddRow(testFundsID, "EQUITY"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		`{"idempotency_key":"k","account_id":"`+testTargetID+`","amount_cents":100}`, testUserID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	mustMeetExpectations(t, mock)
}

func TestHandleDeposit_IntoExternalFundsAccount_400(t *testing.T) {
	srv, mock := setupTestServer(t)
	expectDepositPrelude(mock, sqlmock.NewRows([]string{"id", "type"}).AddRow(testTargetID, "EQUITY"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		`{"idempotency_key":"k","account_id":"`+testTargetID+`","amount_cents":100}`, testUserID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	mustMeetExpectations(t, mock)
}

// =========================================================================
// Transfers (user scoping & balance guard)
// =========================================================================

func transferBody(key, from, to string, amount string) string {
	return `{"idempotency_key":"` + key + `","source_account_id":"` + from +
		`","target_account_id":"` + to + `","amount_cents":` + amount + `,"description":"t"}`
}

func TestHandleTransfer_Unauthorized_NoDBTransaction(t *testing.T) {
	srv, mock := setupTestServer(t)
	w := httptest.NewRecorder()
	srv.handleTransfer(w, authedRequest(http.MethodPost, "/api/v1/transfers",
		transferBody("k", testTargetID, testOtherID, "100"), ""))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	mustMeetExpectations(t, mock) // BEGIN must not be issued
}

func TestHandleTransfer_InsufficientFunds_422(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(reIdem).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(reLockTr).WithArgs(testUserID, testTargetID, testOtherID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "balance_cents", "name", "type"}).
			AddRow(testTargetID, int64(500), "Banco Estado", "ASSET").
			AddRow(testOtherID, int64(0), "Efectivo", "ASSET"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	srv.handleTransfer(w, authedRequest(http.MethodPost, "/api/v1/transfers",
		transferBody("k", testTargetID, testOtherID, "501"), testUserID))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
	mustMeetExpectations(t, mock) // no entries, no balance updates
}

func TestHandleTransfer_TargetOfAnotherUser_404(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(reIdem).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(reLockTr).WillReturnRows(sqlmock.NewRows([]string{"id", "balance_cents", "name", "type"}).
		AddRow(testTargetID, int64(10000), "Banco Estado", "ASSET"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	srv.handleTransfer(w, authedRequest(http.MethodPost, "/api/v1/transfers",
		transferBody("k", testTargetID, testOtherID, "100"), testUserID))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	mustMeetExpectations(t, mock)
}

func TestHandleTransfer_FromExternalFunds_400(t *testing.T) {
	srv, mock := setupTestServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery(reIdem).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(reLockTr).WillReturnRows(sqlmock.NewRows([]string{"id", "balance_cents", "name", "type"}).
		AddRow(testFundsID, int64(-10000), "Capital / Fondos Propios", "EQUITY").
		AddRow(testTargetID, int64(0), "Banco Estado", "ASSET"))
	mock.ExpectRollback()

	w := httptest.NewRecorder()
	srv.handleTransfer(w, authedRequest(http.MethodPost, "/api/v1/transfers",
		transferBody("k", testFundsID, testTargetID, "100"), testUserID))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	mustMeetExpectations(t, mock)
}

func TestHandleTransfer_Success_DoubleEntryBalanced(t *testing.T) {
	srv, mock := setupTestServer(t)
	const amount = int64(3000)
	mock.ExpectBegin()
	mock.ExpectQuery(reIdem).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(reLockTr).WillReturnRows(sqlmock.NewRows([]string{"id", "balance_cents", "name", "type"}).
		AddRow(testTargetID, int64(3000), "Banco Estado", "ASSET").
		AddRow(testOtherID, int64(0), "Efectivo", "ASSET"))
	mock.ExpectQuery(reInsertTx).WithArgs(txReference(testUserID, "k"), "t", testUserID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tx-9"))
	mock.ExpectExec(reInsertLine).WithArgs("tx-9", testTargetID, "DEBIT", amount).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reInsertLine).WithArgs("tx-9", testOtherID, "CREDIT", amount).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reDebitBal).WithArgs(amount, testTargetID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(reCreditBal).WithArgs(amount, testOtherID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	w := httptest.NewRecorder()
	// Exact full balance must be allowed (balance == amount).
	srv.handleTransfer(w, authedRequest(http.MethodPost, "/api/v1/transfers",
		transferBody("k", testTargetID, testOtherID, "3000"), testUserID))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	mustMeetExpectations(t, mock)
}

// =========================================================================
// Pages
// =========================================================================

func TestProtectedPage_RedirectsWithoutSession(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	srv.protectedPage("dashboard.html")(w, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect to /login, got %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestProtectedPage_ServesWithValidCookie(t *testing.T) {
	srv, _ := setupTestServer(t)
	token, err := GenerateToken(testUserID, "a@b.cl")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: AuthCookieName, Value: token})
	w := httptest.NewRecorder()
	srv.protectedPage("dashboard.html")(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("expected embedded page, got %d", w.Code)
	}
}

func TestEmbeddedPages_NoTechnicalJargon(t *testing.T) {
	banned := []string{"PostgreSQL", "Redis", "ACID", "Mathematically Balanced", "invariante"}
	for _, page := range []string{"web/login.html", "web/dashboard.html", "web/transferencias.html", "web/estres.html"} {
		data, err := webFS.ReadFile(page)
		if err != nil {
			t.Fatalf("missing embedded page %s: %v", page, err)
		}
		for _, word := range banned {
			if strings.Contains(string(data), word) {
				t.Errorf("%s contains banned term %q", page, word)
			}
		}
	}
}
