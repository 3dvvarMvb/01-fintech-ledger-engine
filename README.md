# Project 1: Fintech High-Throughput Ledger Engine

Motor transaccional inmutable para sistemas bancarios y pasarelas de pago basado en el principio contable de partida doble (**Double-Entry Bookkeeping**) con consistencia transaccional ACID estricta en PostgreSQL y Redis.

## Arquitectura y Stack Tecnológico
- **Lenguaje**: Go (Golang 1.23)
- **Persistencia**: PostgreSQL 16 con transacciones serializables y auditoría
- **Caché & Idempotencia**: Redis 7
- **Contenedores**: Docker (Multi-Stage Build < 25MB) & Docker Compose
- **Orquestación**: Kubernetes manifests en `k8s/`
- **Testing**: Pruebas de estrés de concurrencia y validación de idempotencia

## Estructura del Proyecto
```
01-fintech-ledger-engine/
├── services/
│   └── ledger-core/
│       ├── migrations/          # Esquemas SQL inmutables
│       ├── Dockerfile           # Multi-stage build con alpine
│       ├── main.go              # Core HTTP API y transacciones ACID
│       └── go.mod
├── k8s/                         # Manifiestos de Kubernetes
├── docker-compose.yml           # Postgres, Redis y Ledger Core
├── .env.example
└── AGENTS.md                    # Directivas para subagentes
```

## Ejecución Rápida
```bash
# 1. Iniciar servicios con Docker Compose
docker compose up -d --build

# 2. Verificar estado de los contenedores
docker compose ps

# 3. Consultar cuentas contables iniciales
curl http://localhost:8080/api/v1/accounts

# 4. Ejecutar una transferencia atómica con Idempotencia
curl -X POST http://localhost:8080/api/v1/transfers \
  -H "Content-Type: application/json" \
  -d '{
    "idempotency_key": "tx-uuid-test-001",
    "source_account_id": "a0000000-0000-0000-0000-000000000001",
    "target_account_id": "a0000000-0000-0000-0000-000000000002",
    "amount_cents": 50000,
    "description": "Payment settlement #001"
  }'
```
