# Project Directives: Fintech High-Throughput Ledger Engine

Este proyecto implementa un libro contable financiero inmutable basado en el principio de partida doble (Double-Entry Bookkeeping) con alta concurrencia y consistencia ACID estricta.

## Responsabilidades de los Subagentes en este Proyecto
- **system-architect**: Diseñar modelos de datos de cuentas (Activos, Pasivos, Capital, Ingresos, Gastos) asegurando la invariante fundamental: `Sum(Débitos) == Sum(Créditos)`.
- **backend-engineer**: Desarrollar en Go (`services/ledger-core/`) utilizando el paquete estándar `database/sql` o `pgx` con transacciones atómicas `BEGIN ... COMMIT` y bloqueos pesimistas / distribuidos en Redis.
- **frontend-engineer**: Implementar en Next.js (`frontend/`) visualizaciones de balances de cuentas y transacciones con filtrado por fecha y auditoría.
- **devops-sre-engineer**: Optimizar Dockerfiles con binarios estáticos en Scratch/Alpine, y mantener los manifiestos en `k8s/`.
- **qa-security-auditor**: Crear tests de estrés de concurrencia para asegurar que transferencias simultáneas entre las mismas cuentas no causen saldos negativos ni condiciones de carrera (Race Conditions).
