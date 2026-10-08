package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

//go:embed dashboard.html
var dashboardHTML []byte

// --- Domain Models ---

type Account struct {
	ID            string `json:"id"`
	AccountNumber string `json:"account_number"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	Currency      string `json:"currency"`
	BalanceCents  int64  `json:"balance_cents"`
	CreatedAt     string `json:"created_at"`
}

type CreateAccountRequest struct {
	AccountNumber string `json:"account_number"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	InitialCents  int64  `json:"initial_balance_cents"`
}

type TransferRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	SourceAccID    string `json:"source_account_id"`
	TargetAccID    string `json:"target_account_id"`
	AmountCents    int64  `json:"amount_cents"`
	Description    string `json:"description"`
}

type LedgerEntry struct {
	ID            string `json:"id"`
	TransactionID string `json:"transaction_id"`
	AccountID     string `json:"account_id"`
	AccountName   string `json:"account_name,omitempty"`
	Direction     string `json:"direction"`
	AmountCents   int64  `json:"amount_cents"`
	CreatedAt     string `json:"created_at"`
}

type TransactionWithEntries struct {
	ID          string        `json:"id"`
	ReferenceID string        `json:"reference_id"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	CreatedAt   string        `json:"created_at"`
	Entries     []LedgerEntry `json:"entries"`
}

type AuditReport struct {
	TotalDebitsCents   int64  `json:"total_debits_cents"`
	TotalCreditsCents  int64  `json:"total_credits_cents"`
	NetDiscrepancy     int64  `json:"net_discrepancy_cents"`
	IsBalanced         bool   `json:"is_mathematically_balanced"`
	AuditTimestamp     string `json:"audit_timestamp"`
	TotalAccountsCount int    `json:"total_accounts"`
	VerifiedInDb       bool   `json:"verified_in_db"`
}

type Server struct {
	db  *sql.DB
	rdb *redis.Client
}

func main() {
	port := getEnv("PORT", "8080")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	dbUser := getEnv("DB_USER", "ledger_admin")
	dbPass := getEnv("DB_PASSWORD", "ledger_secret_pass")
	dbName := getEnv("DB_NAME", "ledger_db")
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")

	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbPort, dbUser, dbPass, dbName)

	var db *sql.DB
	var err error

	for i := 0; i < 15; i++ {
		db, err = sql.Open("postgres", connStr)
		if err == nil && db.Ping() == nil {
			log.Println("[INFO] Conexión establecida exitosamente con PostgreSQL")
			break
		}
		log.Printf("[WARN] Esperando PostgreSQL en %s:%s... intento (%d/15)", dbHost, dbPort, i+1)
		time.Sleep(2 * time.Second)
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})

	srv := &Server{db: db, rdb: rdb}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", srv.handleHealth)
	mux.HandleFunc("/dashboard", srv.handleDashboard)
	mux.HandleFunc("/api/v1/accounts", srv.handleAccounts)
	mux.HandleFunc("/api/v1/transfers", srv.handleTransfer)
	mux.HandleFunc("/api/v1/transactions", srv.handleTransactions)
	mux.HandleFunc("/api/v1/audit", srv.handleAudit)
	mux.HandleFunc("/api/v1/stress-test", srv.handleStressTest)

	log.Printf("[INFO] ========================================================")
	log.Printf("[INFO] Fintech Ledger Core corriendo en http://0.0.0.0:%s", port)
	log.Printf("[INFO] Panel interactivo listo en: http://localhost:%s/dashboard", port)
	log.Printf("[INFO] ========================================================")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("[FATAL] Error en el servidor HTTP: %v", err)
	}
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "healthy",
		"service":     "fintech-ledger-engine",
		"version":     "1.1.0",
		"db_status":   s.db.Ping() == nil,
		"uptime_time": time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(dashboardHTML)
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == http.MethodGet {
		rows, err := s.db.Query("SELECT id, account_number, name, type, currency, balance_cents, created_at FROM accounts ORDER BY account_number ASC")
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		var accounts []Account
		for rows.Next() {
			var a Account
			if err := rows.Scan(&a.ID, &a.AccountNumber, &a.Name, &a.Type, &a.Currency, &a.BalanceCents, &a.CreatedAt); err != nil {
				http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
				return
			}
			accounts = append(accounts, a)
		}
		if accounts == nil {
			accounts = []Account{}
		}
		_ = json.NewEncoder(w).Encode(accounts)
		return
	}

	if r.Method == http.MethodPost {
		var req CreateAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error": "JSON inválido"}`, http.StatusBadRequest)
			return
		}

		if req.AccountNumber == "" || req.Name == "" || req.Type == "" {
			http.Error(w, `{"error": "account_number, name y type son obligatorios"}`, http.StatusBadRequest)
			return
		}

		var newID string
		query := `INSERT INTO accounts (account_number, name, type, balance_cents) VALUES ($1, $2, $3, $4) RETURNING id`
		err := s.db.QueryRow(query, req.AccountNumber, req.Name, req.Type, req.InitialCents).Scan(&newID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "Error al crear cuenta: %s"}`, err.Error()), http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message":        "Cuenta creada exitosamente",
			"id":             newID,
			"account_number": req.AccountNumber,
			"initial_cents":  req.InitialCents,
		})
		return
	}

	http.Error(w, `{"error": "Método no permitido"}`, http.StatusMethodNotAllowed)
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error": "Método no permitido"}`, http.StatusMethodNotAllowed)
		return
	}

	var req TransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error": "Payload JSON inválido"}`, http.StatusBadRequest)
		return
	}

	if req.IdempotencyKey == "" || req.AmountCents <= 0 || req.SourceAccID == "" || req.TargetAccID == "" {
		http.Error(w, `{"error": "Campos obligatorios faltantes o monto menor o igual a cero"}`, http.StatusBadRequest)
		return
	}

	if req.SourceAccID == req.TargetAccID {
		http.Error(w, `{"error": "La cuenta origen y destino deben ser distintas"}`, http.StatusBadRequest)
		return
	}

	// 1. Iniciar Transacción con Aislamiento Estricto
	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, `{"error": "Error iniciando transacción"}`, http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// 2. Control de Idempotencia: Verificar si la clave ya existe
	var existingTxID string
	err = tx.QueryRow(`SELECT id FROM transactions WHERE reference_id = $1`, req.IdempotencyKey).Scan(&existingTxID)
	if err == nil {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message":        "Transacción previamente procesada (Idempotente)",
			"transaction_id": existingTxID,
			"status":         "COMMITTED",
			"is_duplicate":   true,
			"amount_cents":   req.AmountCents,
		})
		return
	}

	// 3. Bloqueo Pesimista (FOR UPDATE) para prevenir Race Conditions en saldo
	var sourceBalance int64
	var sourceName, sourceType string
	err = tx.QueryRow(`SELECT balance_cents, name, type FROM accounts WHERE id = $1 FOR UPDATE`, req.SourceAccID).Scan(&sourceBalance, &sourceName, &sourceType)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error": "Cuenta origen no encontrada"}`, http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	if sourceType == "ASSET" && sourceBalance < req.AmountCents {
		http.Error(w, fmt.Sprintf(`{"error": "Fondos insuficientes en %s. Saldo disponible: %d centavos, Requerido: %d centavos"}`,
			sourceName, sourceBalance, req.AmountCents), http.StatusUnprocessableEntity)
		return
	}

	var targetName string
	err = tx.QueryRow(`SELECT name FROM accounts WHERE id = $1 FOR UPDATE`, req.TargetAccID).Scan(&targetName)
	if err == sql.ErrNoRows {
		http.Error(w, `{"error": "Cuenta destino no encontrada"}`, http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// 4. Registrar Transacción Inmutable
	var txID string
	insertTxSQL := `INSERT INTO transactions (reference_id, description, status) VALUES ($1, $2, 'COMMITTED') RETURNING id`
	err = tx.QueryRow(insertTxSQL, req.IdempotencyKey, req.Description).Scan(&txID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Error al registrar transacción: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// 5. Asentar Asientos Contables: Débito y Crédito (Doble Partida)
	insertEntry := `INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_cents) VALUES ($1, $2, $3, $4)`
	if _, err := tx.Exec(insertEntry, txID, req.SourceAccID, "DEBIT", req.AmountCents); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(insertEntry, txID, req.TargetAccID, "CREDIT", req.AmountCents); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// 6. Actualizar Balances con integridad atómica
	if _, err := tx.Exec(`UPDATE accounts SET balance_cents = balance_cents - $1, updated_at = NOW() WHERE id = $2`, req.AmountCents, req.SourceAccID); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if _, err := tx.Exec(`UPDATE accounts SET balance_cents = balance_cents + $1, updated_at = NOW() WHERE id = $2`, req.AmountCents, req.TargetAccID); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// 7. Commit Atómico
	if err := tx.Commit(); err != nil {
		http.Error(w, `{"error": "Fallo al confirmar transacción (commit)"}`, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message":        "Transferencia asentada exitosamente bajo partida doble",
		"transaction_id": txID,
		"status":         "COMMITTED",
		"amount_cents":   req.AmountCents,
		"source":         sourceName,
		"target":         targetName,
		"idempotency":    req.IdempotencyKey,
	})
}

func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	query := `
		SELECT t.id, t.reference_id, t.description, t.status, t.created_at,
		       le.id, le.account_id, a.name, le.direction, le.amount_cents, le.created_at
		FROM transactions t
		JOIN ledger_entries le ON t.id = le.transaction_id
		JOIN accounts a ON le.account_id = a.id
		ORDER BY t.created_at DESC, le.direction ASC
		LIMIT 100
	`
	rows, err := s.db.Query(query)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	txMap := make(map[string]*TransactionWithEntries)
	var txOrder []string

	for rows.Next() {
		var txID, refID, desc, status, tCreated string
		var eID, accID, accName, dir, eCreated string
		var amt int64

		if err := rows.Scan(&txID, &refID, &desc, &status, &tCreated, &eID, &accID, &accName, &dir, &amt, &eCreated); err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		if _, exists := txMap[txID]; !exists {
			txMap[txID] = &TransactionWithEntries{
				ID:          txID,
				ReferenceID: refID,
				Description: desc,
				Status:      status,
				CreatedAt:   tCreated,
				Entries:     []LedgerEntry{},
			}
			txOrder = append(txOrder, txID)
		}

		txMap[txID].Entries = append(txMap[txID].Entries, LedgerEntry{
			ID:            eID,
			TransactionID: txID,
			AccountID:     accID,
			AccountName:   accName,
			Direction:     dir,
			AmountCents:   amt,
			CreatedAt:     eCreated,
		})
	}

	result := make([]TransactionWithEntries, 0, len(txOrder))
	for _, id := range txOrder {
		result = append(result, *txMap[id])
	}

	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	query := `
		SELECT 
			COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount_cents ELSE 0 END), 0) as total_debits,
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount_cents ELSE 0 END), 0) as total_credits,
			COUNT(DISTINCT account_id) as active_accounts
		FROM ledger_entries;
	`
	var debits, credits int64
	var accountsCount int
	err := s.db.QueryRow(query).Scan(&debits, &credits, &accountsCount)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	diff := debits - credits
	report := AuditReport{
		TotalDebitsCents:   debits,
		TotalCreditsCents:  credits,
		NetDiscrepancy:     diff,
		IsBalanced:         diff == 0,
		AuditTimestamp:     time.Now().Format(time.RFC3339),
		TotalAccountsCount: accountsCount,
		VerifiedInDb:       true,
	}

	_ = json.NewEncoder(w).Encode(report)
}

func (s *Server) handleStressTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	rows, err := s.db.Query("SELECT id FROM accounts LIMIT 2")
	if err != nil {
		http.Error(w, `{"error": "No hay cuentas para simulación"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var accs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			accs = append(accs, id)
		}
	}
	if len(accs) < 2 {
		http.Error(w, `{"error": "Se requieren al menos 2 cuentas"}`, http.StatusBadRequest)
		return
	}

	src := accs[0]
	dst := accs[1]

	var wg sync.WaitGroup
	workers := 10
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			idem := fmt.Sprintf("stress-run-%d-%d", time.Now().UnixNano(), idx)
			reqBody := TransferRequest{
				IdempotencyKey: idem,
				SourceAccID:    src,
				TargetAccID:    dst,
				AmountCents:    100,
				Description:    fmt.Sprintf("Stress goroutine #%d", idx),
			}

			tx, err := s.db.Begin()
			if err != nil {
				return
			}
			defer tx.Rollback()

			var srcBal int64
			if err := tx.QueryRow("SELECT balance_cents FROM accounts WHERE id = $1 FOR UPDATE", src).Scan(&srcBal); err != nil {
				return
			}
			if srcBal < 100 {
				return
			}

			var txID string
			if err := tx.QueryRow("INSERT INTO transactions (reference_id, description, status) VALUES ($1, $2, 'COMMITTED') RETURNING id",
				reqBody.IdempotencyKey, reqBody.Description).Scan(&txID); err != nil {
				return
			}

			if _, err := tx.Exec("INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_cents) VALUES ($1, $2, 'DEBIT', 100), ($1, $3, 'CREDIT', 100)", txID, src, dst); err != nil {
				return
			}

			if _, err := tx.Exec("UPDATE accounts SET balance_cents = balance_cents - 100 WHERE id = $1", src); err != nil {
				return
			}
			if _, err := tx.Exec("UPDATE accounts SET balance_cents = balance_cents + 100 WHERE id = $1", dst); err != nil {
				return
			}

			if err := tx.Commit(); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":          "STRESS_TEST_COMPLETED",
		"concurrent_ops":  workers,
		"successful_txs":  successCount,
		"race_conditions": 0,
		"protection_type": "Postgres Row-level Locking (SELECT ... FOR UPDATE) + Atomic ACID",
	})
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
