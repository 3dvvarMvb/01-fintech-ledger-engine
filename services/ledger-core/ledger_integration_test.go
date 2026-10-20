package main

// Concurrency stress tests against a real PostgreSQL.
// They are skipped unless LEDGER_TEST_DSN is set, e.g.:
//
//	LEDGER_TEST_DSN="host=localhost port=5432 user=ledger_admin password=ledger_secret_pass dbname=ledger_db sslmode=disable" \
//	  go test -run Integration -v ./...
//
// Each run creates a fresh user with a random email; the ledger is immutable,
// so test data is intentionally not deleted.

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func integrationServer(t *testing.T) (*Server, string, map[string]string) {
	t.Helper()
	dsn := os.Getenv("LEDGER_TEST_DSN")
	if dsn == "" {
		t.Skip("LEDGER_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil || db.Ping() != nil {
		t.Skipf("PostgreSQL not reachable: %v", err)
	}
	db.SetMaxOpenConns(30)
	t.Cleanup(func() { db.Close() })
	if err := runAutoMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	email := fmt.Sprintf("stress-%d@test.local", time.Now().UnixNano())
	user, err := RegisterUser(db, email, "x", "Stress")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := ensurePersonalAccounts(db, user.ID); err != nil {
		t.Fatalf("provision: %v", err)
	}
	rows, err := db.Query(`SELECT id, name FROM accounts WHERE user_id = $1`, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := map[string]string{}
	for rows.Next() {
		var id, name string
		_ = rows.Scan(&id, &name)
		ids[name] = id
	}
	if len(ids) != len(personalAccounts) {
		t.Fatalf("expected %d accounts, got %d", len(personalAccounts), len(ids))
	}
	return &Server{db: db}, user.ID, ids
}

func deposit(t *testing.T, srv *Server, userID, accID string, cents int64) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.handleDeposit(w, authedRequest(http.MethodPost, "/api/v1/deposits",
		fmt.Sprintf(`{"idempotency_key":"seed-%d","account_id":"%s","amount_cents":%d}`, time.Now().UnixNano(), accID, cents), userID))
	if w.Code != http.StatusCreated {
		t.Fatalf("seed deposit failed: %d %s", w.Code, w.Body.String())
	}
}

func transfer(srv *Server, userID, key, from, to string, cents int64) int {
	w := httptest.NewRecorder()
	srv.handleTransfer(w, authedRequest(http.MethodPost, "/api/v1/transfers",
		fmt.Sprintf(`{"idempotency_key":"%s","source_account_id":"%s","target_account_id":"%s","amount_cents":%d,"description":"stress"}`,
			key, from, to, cents), userID))
	return w.Code
}

func assertLedgerInvariants(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	var debits, credits int64
	if err := db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN le.direction='DEBIT' THEN le.amount_cents END),0),
		COALESCE(SUM(CASE WHEN le.direction='CREDIT' THEN le.amount_cents END),0)
		FROM ledger_entries le JOIN accounts a ON a.id = le.account_id WHERE a.user_id = $1`, userID).
		Scan(&debits, &credits); err != nil {
		t.Fatal(err)
	}
	if debits != credits {
		t.Fatalf("double-entry broken: debits=%d credits=%d", debits, credits)
	}
	var negatives int
	_ = db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE user_id=$1 AND type='ASSET' AND balance_cents < 0`, userID).Scan(&negatives)
	if negatives > 0 {
		t.Fatalf("%d asset accounts ended with a negative balance", negatives)
	}
	// Stored balances must match the sum of their entries (no lost updates).
	var drift int
	_ = db.QueryRow(`SELECT COUNT(*) FROM accounts a WHERE a.user_id=$1 AND a.balance_cents <>
		COALESCE((SELECT SUM(CASE WHEN direction='CREDIT' THEN amount_cents ELSE -amount_cents END)
		          FROM ledger_entries WHERE account_id=a.id),0)`, userID).Scan(&drift)
	if drift > 0 {
		t.Fatalf("%d accounts have a stored balance that does not match their ledger entries", drift)
	}
}

// 50 concurrent withdrawals competing for funds that only cover 10.
func TestIntegration_ConcurrentTransfers_NoOverdraft(t *testing.T) {
	srv, userID, ids := integrationServer(t)
	from, to := ids["Banco Falabella"], ids["Efectivo"]
	deposit(t, srv, userID, from, 10_000)

	const workers = 50
	codes := make(chan int, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes <- transfer(srv, userID, fmt.Sprintf("od-%d", i), from, to, 1_000)
		}(i)
	}
	close(start)
	wg.Wait()
	close(codes)

	count := map[int]int{}
	for c := range codes {
		count[c]++
	}
	if count[http.StatusCreated] != 10 || count[http.StatusUnprocessableEntity] != 40 {
		t.Fatalf("expected 10 accepted / 40 rejected, got %v", count)
	}
	var bal int64
	_ = srv.db.QueryRow(`SELECT balance_cents FROM accounts WHERE id=$1`, from).Scan(&bal)
	if bal != 0 {
		t.Fatalf("expected source balance 0, got %d", bal)
	}
	assertLedgerInvariants(t, srv.db, userID)
}

// Opposite-direction transfers between the same two accounts must not deadlock.
func TestIntegration_BidirectionalTransfers_NoDeadlock(t *testing.T) {
	srv, userID, ids := integrationServer(t)
	a, b := ids["Banco Falabella"], ids["Banco Estado"]
	deposit(t, srv, userID, a, 100_000)
	deposit(t, srv, userID, b, 100_000)

	const perSide = 40
	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := map[int]int{}
	start := make(chan struct{})
	for i := 0; i < perSide*2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			src, dst := a, b
			if i%2 == 1 {
				src, dst = b, a
			}
			if code := transfer(srv, userID, fmt.Sprintf("bi-%d", i), src, dst, 500); code != http.StatusCreated {
				mu.Lock()
				failures[code]++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("expected all transfers to succeed (no deadlocks), failures by status: %v", failures)
	}
	var sum int64
	_ = srv.db.QueryRow(`SELECT SUM(balance_cents) FROM accounts WHERE id IN ($1,$2)`, a, b).Scan(&sum)
	if sum != 200_000 {
		t.Fatalf("money was created or destroyed: total=%d", sum)
	}
	assertLedgerInvariants(t, srv.db, userID)
}

// The same idempotency key sent concurrently must be applied at most once.
func TestIntegration_ConcurrentIdempotentReplays(t *testing.T) {
	srv, userID, ids := integrationServer(t)
	from, to := ids["Ahorros"], ids["Efectivo"]
	deposit(t, srv, userID, from, 50_000)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			transfer(srv, userID, "same-key", from, to, 1_000)
		}()
	}
	close(start)
	wg.Wait()

	var n int
	_ = srv.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE reference_id=$1`, txReference(userID, "same-key")).Scan(&n)
	if n != 1 {
		t.Fatalf("expected exactly 1 transaction for the key, got %d", n)
	}
	var bal int64
	_ = srv.db.QueryRow(`SELECT balance_cents FROM accounts WHERE id=$1`, from).Scan(&bal)
	if bal != 49_000 {
		t.Fatalf("expected 49000 after a single debit, got %d", bal)
	}
	assertLedgerInvariants(t, srv.db, userID)
}
