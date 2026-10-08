-- Migration: 001_init_ledger.sql
-- Inmutable Double-Entry Bookkeeping Ledger

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Account Types Enum
DO $$ BEGIN
    CREATE TYPE account_type AS ENUM ('ASSET', 'LIABILITY', 'EQUITY', 'REVENUE', 'EXPENSE');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE transaction_status AS ENUM ('PENDING', 'COMMITTED', 'REJECTED');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

DO $$ BEGIN
    CREATE TYPE entry_direction AS ENUM ('DEBIT', 'CREDIT');
EXCEPTION
    WHEN duplicate_object THEN null;
END $$;

-- Accounts Table
CREATE TABLE IF NOT EXISTS accounts (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_number VARCHAR(64) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    type account_type NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    balance_cents BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Inmutable Transactions Header
CREATE TABLE IF NOT EXISTS transactions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    reference_id VARCHAR(128) UNIQUE NOT NULL, -- Idempotency key
    description TEXT NOT NULL,
    status transaction_status NOT NULL DEFAULT 'PENDING',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Ledger Entries (Double-Entry lines)
CREATE TABLE IF NOT EXISTS ledger_entries (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    transaction_id UUID NOT NULL REFERENCES transactions(id) ON DELETE RESTRICT,
    account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    direction entry_direction NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_ledger_entries_account_id ON ledger_entries(account_id);
CREATE INDEX IF NOT EXISTS idx_ledger_entries_transaction_id ON ledger_entries(transaction_id);
CREATE INDEX IF NOT EXISTS idx_transactions_reference_id ON transactions(reference_id);

-- Initial Mock Data for Testing
INSERT INTO accounts (id, account_number, name, type, currency, balance_cents)
VALUES 
    ('a0000000-0000-0000-0000-000000000001', 'ACC-1001', 'Cash & Bank Reserves', 'ASSET', 'USD', 100000000), -- $1,000,000.00
    ('a0000000-0000-0000-0000-000000000002', 'ACC-2001', 'User Deposits Liability', 'LIABILITY', 'USD', 50000000),  -- $500,000.00
    ('a0000000-0000-0000-0000-000000000003', 'ACC-3001', 'Operating Revenues', 'REVENUE', 'USD', 50000000)
ON CONFLICT (account_number) DO NOTHING;

-- Initial Seed Balanced Transaction
INSERT INTO transactions (id, reference_id, description, status)
VALUES 
    ('t0000000-0000-0000-0000-000000000001', 'tx-init-capital-deposit', 'Initial Capital Reserve Injection', 'COMMITTED')
ON CONFLICT (reference_id) DO NOTHING;

INSERT INTO ledger_entries (id, transaction_id, account_id, direction, amount_cents)
VALUES 
    ('e0000000-0000-0000-0000-000000000001', 't0000000-0000-0000-0000-000000000001', 'a0000000-0000-0000-0000-000000000001', 'DEBIT', 50000000),
    ('e0000000-0000-0000-0000-000000000002', 't0000000-0000-0000-0000-000000000001', 'a0000000-0000-0000-0000-000000000002', 'CREDIT', 50000000)
ON CONFLICT DO NOTHING;
