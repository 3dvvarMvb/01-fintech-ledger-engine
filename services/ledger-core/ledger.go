package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// personalAccountSpec describes the accounts every user receives automatically.
type personalAccountSpec struct {
	Code string
	Name string
	Type string
}

// Orden de presentación: cuentas visibles primero, cuenta técnica de aportes al final.
var personalAccounts = []personalAccountSpec{
	{"FAL", "Banco Falabella", "ASSET"},
	{"EST", "Banco Estado", "ASSET"},
	{"EFE", "Efectivo", "ASSET"},
	{"AHO", "Ahorros", "ASSET"},
	{"CAP", "Capital / Fondos Propios", "EQUITY"},
}

// externalFundsType is the technical counter-account used by deposits.
const externalFundsType = "EQUITY"

func accountNumberFor(userID, code string) string {
	return "U" + strings.ReplaceAll(userID, "-", "")[:12] + "-" + code
}

func txReference(userID, key string) string {
	return strings.ReplaceAll(userID, "-", "")[:8] + ":" + key
}

// ensurePersonalAccounts creates the default accounts for a user if none exist yet.
// It is idempotent: account_number is unique, so concurrent calls cannot duplicate.
func ensurePersonalAccounts(db *sql.DB, userID string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE user_id = $1`, userID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, spec := range personalAccounts {
		_, err := db.Exec(
			`INSERT INTO accounts (account_number, name, type, currency, balance_cents, user_id)
			 VALUES ($1, $2, $3, 'CLP', 0, $4) ON CONFLICT (account_number) DO NOTHING`,
			accountNumberFor(userID, spec.Code), spec.Name, spec.Type, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

type DepositRequest struct {
	IdempotencyKey  string `json:"idempotency_key"`
	AccountID       string `json:"account_id"`
	TargetAccountID string `json:"target_account_id"`
	AmountCents     int64  `json:"amount_cents"`
	Description     string `json:"description"`
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleDeposit registers money entering one of the user's accounts.
// Double entry: DEBIT the external-funds account, CREDIT the target account.
func (s *Server) handleDeposit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Método no permitido")
		return
	}
	userID, ok := GetUserIDFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req DepositRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Datos inválidos")
		return
	}

	targetAccID := req.TargetAccountID
	if targetAccID == "" {
		targetAccID = req.AccountID
	}
	if targetAccID == "" || req.AmountCents <= 0 {
		writeJSONError(w, http.StatusBadRequest, "Indica la cuenta y un monto mayor a cero")
		return
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = fmt.Sprintf("dep-%d", time.Now().UnixNano())
	}
	if req.Description == "" {
		req.Description = "Ingreso de dinero"
	}

	if err := ensurePersonalAccounts(s.db, userID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "No se pudieron preparar tus cuentas")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error iniciando operación")
		return
	}
	defer tx.Rollback()

	ref := txReference(userID, req.IdempotencyKey)
	var existing string
	if err := tx.QueryRow(`SELECT id FROM transactions WHERE reference_id = $1`, ref).Scan(&existing); err == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"transaction_id": existing, "is_duplicate": true})
		return
	}

	// Bloqueo pesimista de ambas cuentas en orden determinista (evita deadlocks).
	rows, err := tx.Query(`SELECT id, type FROM accounts
		WHERE user_id = $1 AND (id = $2 OR type = $3) ORDER BY id FOR UPDATE`,
		userID, targetAccID, externalFundsType)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error al bloquear cuentas")
		return
	}
	var targetOK bool
	var targetType, fundsID string
	for rows.Next() {
		var id, typ string
		if err := rows.Scan(&id, &typ); err != nil {
			rows.Close()
			writeJSONError(w, http.StatusInternalServerError, "Error leyendo cuentas")
			return
		}
		if typ == externalFundsType {
			fundsID = id
		}
		if id == targetAccID {
			targetOK, targetType = true, typ
		}
	}
	rows.Close()
	if !targetOK {
		writeJSONError(w, http.StatusNotFound, "Cuenta no encontrada")
		return
	}
	if targetType == externalFundsType || fundsID == "" {
		writeJSONError(w, http.StatusBadRequest, "No se puede ingresar dinero a esta cuenta")
		return
	}

	var txID string
	if err := tx.QueryRow(`INSERT INTO transactions (reference_id, description, status, user_id)
		VALUES ($1, $2, 'COMMITTED', $3) RETURNING id`, ref, req.Description, userID).Scan(&txID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error al registrar el ingreso")
		return
	}

	entry := `INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_cents) VALUES ($1, $2, $3, $4)`
	if _, err := tx.Exec(entry, txID, fundsID, "DEBIT", req.AmountCents); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error en asiento contable")
		return
	}
	if _, err := tx.Exec(entry, txID, targetAccID, "CREDIT", req.AmountCents); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error en asiento contable")
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET balance_cents = balance_cents - $1, updated_at = NOW() WHERE id = $2`, req.AmountCents, fundsID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error actualizando saldos")
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET balance_cents = balance_cents + $1, updated_at = NOW() WHERE id = $2`, req.AmountCents, targetAccID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Error actualizando saldos")
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "No se pudo confirmar el ingreso")
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"transaction_id": txID,
		"amount_cents":   req.AmountCents,
		"status":         "COMMITTED",
	})
}

// --- Pages & static assets ---

func (s *Server) servePage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(webFS, "web/"+name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}

// protectedPage redirects to /login when there is no valid session.
func (s *Server) protectedPage(name string) http.HandlerFunc {
	page := s.servePage(name)
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := ValidateToken(ExtractToken(r)); err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		page(w, r)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: AuthCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, err := ValidateToken(ExtractToken(r)); err == nil {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
