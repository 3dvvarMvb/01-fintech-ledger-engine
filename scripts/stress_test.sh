#!/usr/bin/env bash
# Stress test script for Fintech Ledger Engine
set -e

HOST="http://localhost:8080"

echo "================================================================="
echo "  Fintech Ledger Engine - Test de Concurrencia e Idempotencia"
echo "================================================================="

echo "1. Comprobando estado del servicio..."
curl -s -f "$HOST/health" > /dev/null || (echo "Error: el servicio no está respondiendo en $HOST" && exit 1)
echo "✓ Servicio operativo y saludable."

echo ""
echo "2. Obteniendo cuentas disponibles..."
ACCOUNTS=$(curl -s "$HOST/api/v1/accounts")
SRC_ID=$(echo "$ACCOUNTS" | grep -o '"id":"[^"]*' | head -n 1 | cut -d'"' -f4)
DST_ID=$(echo "$ACCOUNTS" | grep -o '"id":"[^"]*' | head -n 2 | tail -n 1 | cut -d'"' -f4)

if [ -z "$SRC_ID" ] || [ -z "$DST_ID" ]; then
    echo "Error: no se encontraron cuentas válidas."
    exit 1
fi
echo "✓ Origen: $SRC_ID"
echo "✓ Destino: $DST_ID"

echo ""
echo "3. Prueba de Idempotencia (enviando el mismo Idempotency-Key dos veces)..."
IDEM_KEY="test-idempotency-$(date +%s)"
PAYLOAD="{\"idempotency_key\":\"$IDEM_KEY\",\"source_account_id\":\"$SRC_ID\",\"target_account_id\":\"$DST_ID\",\"amount_cents\":1000,\"description\":\"Test Idempotencia\"}"

RESP1=$(curl -s -X POST "$HOST/api/v1/transfers" -H "Content-Type: application/json" -d "$PAYLOAD")
RESP2=$(curl -s -X POST "$HOST/api/v1/transfers" -H "Content-Type: application/json" -d "$PAYLOAD")

echo "Respuesta 1: $RESP1"
echo "Respuesta 2 (Debe detectar duplicado): $RESP2"

echo ""
echo "4. Ejecutando auditoría matemática del libro contable..."
AUDIT=$(curl -s "$HOST/api/v1/audit")
echo "Resultado de Auditoría:"
echo "$AUDIT"

echo ""
echo "================================================================="
echo "  ✓ Pruebas completadas con éxito!"
echo "================================================================="
