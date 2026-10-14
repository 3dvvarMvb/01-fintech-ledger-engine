# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Next.js (App Router), Tailwind CSS, shadcn/ui en frontend; Go 1.23 (`services/ledger-core/`), PostgreSQL 16 y Redis 7 en backend.

## Users

Solución híbrida para dos perfiles clave que interactúan con la infraestructura financiera:
1. **Ejecutivos de Tesorería y Operadores Financieros**: Gestionan liquidez, aprueban transferencias atómicas interbancarias, consultan balances contables y auditan la conciliación de activos y pasivos.
2. **Ingenieros de Plataforma y DevOps/SRE**: Supervisan la concurrencia en tiempo real, garantizan la consistencia ACID estricta, verifican la prevención de race conditions y auditan la idempotencia distribuida.

## Product Purpose

Fintech High-Throughput Ledger Engine es un sistema de contabilidad financiera inmutable basado en el principio de partida doble (**Double-Entry Bookkeeping**). Su propósito es proporcionar una fuente de verdad financiera infalible donde cada transacción garantiza que los fondos nunca se pierdan ni se dupliquen, eliminando saldos negativos accidentales mediante bloqueos transaccionales pesimistas y manteniendo la discrepancia contable neta en cero en todo momento.

## Positioning

A diferencia de las arquitecturas tradicionales basadas en contabilidad de partida simple o balances agregados asíncronamente (vulnerables a condiciones de carrera bajo concurrencia extrema), este motor aplica transacciones serializables con `SELECT ... FOR UPDATE` en PostgreSQL, validación matemática de partida doble por cada asiento (`Sum(Débitos) == Sum(Créditos)`), y control de idempotencia distribuido en Redis, ofreciendo una pista de auditoría forense inmutable de grado bancario institucional (Stripe Treasury / Brex tier).

## Operating Context

Consola de Treasury Management & Core Banking operando en entornos financieros de alta frecuencia y volumen crítico:
- Conciliación de cuentas de activos (reservas bancarias), pasivos (depósitos de usuarios), capital, ingresos y gastos.
- Transferencias atómicas entre cuentas segregadas con verificación previa de liquidez.
- Auditorías regulatorias instantáneas y simulaciones de estrés de concurrencia bajo demanda.

## Capabilities and Constraints

- **Invariante Contable Inquebrantable**: `Sum(Débitos) == Sum(Créditos)` evaluado en tiempo real. Discrepancia neta siempre `$0.00`.
- **Clasificación Estricta de Cuentas**: `ASSET`, `LIABILITY`, `EQUITY`, `REVENUE`, `EXPENSE`.
- **Protección Antiretiro Negativo**: Las cuentas de tipo `ASSET` rechazan cualquier operación que genere sobregiro.
- **Idempotencia Estricta (Exactly-Once)**: Toda transferencia requiere un `idempotency_key` (UUID v4) indexado para prevenir débitos duplicados.
- **Bloqueo Pesimista en Fila**: Las cuentas involucradas se bloquean atómicamente con `SELECT ... FOR UPDATE` durante la transacción.
- **Inmutabilidad Absoluta**: Asientos registrados no admiten `DELETE` ni `UPDATE`; las correcciones solo proceden vía contra-asientos formales.

## Brand Commitments

- **Identidad**: TreasuryOS — Consola institucional de tesorería y banca privada (estética boutique bancaria suiza en tonos cálidos lino y oliva).
- **Paleta**: Toasted Oat & Olive (`#F7F5F0` base avena/lino cálido, `#FFFFFF` para paneles y tarjetas, `#E6E1D8` para bordes arena suave, `#1C1917` carbón cálido para textos).
- **Acentos Semánticos**:
  - Verde Oliva (`#2D5A3F` / `#3E7B54`): Créditos contables, paridad matemática certificada y botones primarios.
  - Cobre / Terracota (`#C06C3E`): Débitos contables, alertas de sobregiro y discrepancias.
  - Ámbar dorado (`#B45309`): Advertencias y reservas condicionales.
- **Tipografía**: IBM Plex Sans para jerarquía textual y JetBrains Mono con alineación tabular (`tabular-nums`) para todas las cifras financieras, hashes y UUIDs.

## Evidence on Hand

- Microservicio Go operativo en `services/ledger-core/` compilado en contenedor estático Alpine.
- Base de datos PostgreSQL 16 con esquemas inmutables aplicados (`services/ledger-core/migrations/001_init_ledger.sql`).
- Redis 7 para idempotencia y locks en `docker-compose.yml`.
- Endpoints REST verificados: `/health`, `/dashboard`, `/api/v1/accounts`, `/api/v1/transfers`, `/api/v1/transactions`, `/api/v1/audit`, `/api/v1/stress-test`.
- Panel web interactivo desplegado en `services/ledger-core/dashboard.html`.
- Sistema de tokens de diseño documentado en `design-system/core-banking-treasury/MASTER.md`.

## Product Principles

1. **Mathematical Invariant Over Assumption**: El balance contable nunca se presume; se audita y valida explícitamente en cada transacción (`Sum(Dr) == Sum(Cr)`).
2. **Deterministic Immutability**: Los asientos contables son sagrados; ningún registro histórico se altera o suprime.
3. **Pessimistic Safety**: En tesorería institucional, la seguridad y prevención de carreras superan el optimismo concurrente.
4. **Institutional Precision**: Formateo monetario en centavos exactos, números tabulares monoespaciados, claves de idempotencia visibles y confirmación de dos pasos para transferencias.

## Accessibility & Inclusion

- Contraste elevado de texto y cifras cumpliendo WCAG AAA sobre fondos oscuros.
- Cifras con formato tabular (`tabular-nums`) y separadores de miles y decimales explícitos para evitar errores operativos de lectura.
- Navegación por teclado completa, focus visible en todos los controles interactivos y soporte de escape en modales de seguridad.
