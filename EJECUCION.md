# Guía de Ejecución Rápida: Fintech High-Throughput Ledger Engine

Bienvenido a la guía oficial de ejecución del **Proyecto 1: Fintech Ledger Engine**. Este proyecto fue diseñado como una pieza de ingeniería de nivel empresarial para demostrar dominio de **sistemas distribuidos, transacciones ACID estrictas, consistencia financiera de partida doble y prevención de condiciones de carrera**.

---

## 1. Puesta en Marcha (1 Solo Comando)

Desde esta carpeta (`01-fintech-ledger-engine/`) o desde la raíz del workspace, ejecuta:

```bash
docker compose up -d --build
```

Esto descargará y levantará automáticamente en segundo plano:
1. **PostgreSQL 16**: Base de datos relacional con migraciones iniciales de cuentas y partida doble aplicadas (`puerto 5432`).
2. **Redis 7**: Motor en memoria para bloqueos y caché (`puerto 6379`).
3. **Ledger Core (Go 1.23)**: Microservicio compilado en imagen ligera Alpine con healthchecks activos (`puerto 8080`).

Para verificar que todos los contenedores estén saludables:
```bash
docker compose ps
```

---

## 2. Panel de Control Web Interactivo

Abre tu navegador web y entra a:
👉 **[http://localhost:8080/dashboard](http://localhost:8080/dashboard)**

En este panel visual podrás:
- **Ver saldos en tiempo real**: Formateados en dólares con clasificación contable (Activos, Pasivos, Ingresos).
- **Ejecutar Transferencias Atómicas**: Seleccionando cuenta origen, destino y monto, con generación automática de claves de idempotencia UUID.
- **Consultar el Libro Contable (Audit Trail)**: Tabla en vivo que muestra cada débito (`-`) y crédito (`+`) con marca de tiempo y referencia.
- **Auditar el Sistema en 1 Clic**: Botón *"Auditar Libro"* que verifica la invariante matemática `Sum(Débitos) == Sum(Créditos)` (debe indicar `EQUILIBRADA`).
- **Simular Concurrencia**: Botón *"Simular Concurrencia"* que dispara goroutines simultáneas para demostrar que no existen condiciones de carrera ni saldos negativos.

---

## 3. Guía de Endpoints API (Probados con `curl`)

Si prefieres interactuar mediante terminal o Postman:

### A. Healthcheck del Microservicio
```bash
curl -s http://localhost:8080/health | jq
```

### B. Consultar Cuentas y Balances
```bash
curl -s http://localhost:8080/api/v1/accounts | jq
```

### C. Ejecutar una Transferencia con Idempotencia
```bash
curl -s -X POST http://localhost:8080/api/v1/transfers \
  -H "Content-Type: application/json" \
  -d '{
    "idempotency_key": "tx-demo-001",
    "source_account_id": "a0000000-0000-0000-0000-000000000001",
    "target_account_id": "a0000000-0000-0000-0000-000000000002",
    "amount_cents": 25000,
    "description": "Pago de servicios profesionales"
  }' | jq
```
> *Prueba de idempotencia*: Si ejecutas exactamente el mismo comando anterior por segunda vez, el servidor detectará que la clave ya fue procesada y no duplicará el cobro ni alterará el saldo.

### D. Consultar el Asiento Contable (Historial de Transacciones)
```bash
curl -s http://localhost:8080/api/v1/transactions | jq
```

### E. Auditoría Matemática del Libro
```bash
curl -s http://localhost:8080/api/v1/audit | jq
```
*Respuesta esperada:*
```json
{
  "total_debits_cents": 50025000,
  "total_credits_cents": 50025000,
  "net_discrepancy_cents": 0,
  "is_mathematically_balanced": true,
  "verified_in_db": true
}
```

---

## 4. Script Automatizado de Estrés y Pruebas

Hemos incluido un script en Bash que ejecuta las validaciones críticas de concurrencia e idempotencia en un solo paso:

```bash
./scripts/stress_test.sh
```

---

## 5. Argumentos Clave para Defender este Proyecto en una Entrevista

Cuando un entrevistador técnico o Tech Lead te pregunte sobre este proyecto:

1. **¿Por qué usar Partida Doble en lugar de solo actualizar una columna `balance`?**
   > *"En sistemas financieros reales, jamás se modifica un saldo sin un registro inmutable de contrapartida. La partida doble garantiza que cada centavo que sale de una cuenta entra a otra. Esto permite reconstruir la historia financiera desde el día cero y auditar discrepancias al instante."*

2. **¿Cómo solucionas las condiciones de carrera (Race Conditions) ante pagos concurrentes?**
   > *"Implementamos bloqueos pesimistas a nivel de fila en PostgreSQL mediante `SELECT ... FOR UPDATE` dentro de una transacción `BEGIN ... COMMIT`. Si dos peticiones intentan debitar de la misma cuenta al mismo tiempo, la segunda espera a que la primera termine y valida si aún quedan fondos suficientes, impidiendo sobregiros o saldos corruptos."*

3. **¿Cómo manejas reintentos de red del cliente (Network Timeouts)?**
   > *"A través de un mecanismo de Idempotency-Key. El cliente envía un UUID único por operación. La base de datos tiene una restricción `UNIQUE(reference_id)`. Si la red falla y el cliente reintenta, el servidor reconoce la transacción previa y responde exitosamente sin volver a debitar dinero."*

4. **¿Por qué la arquitectura en Go y Docker es Cloud-Native?**
   > *"El binario estático de Go se compila en una fase multi-stage y corre sobre una imagen Alpine mínima con usuario no-root. Pesa menos de 25 MB, inicia en menos de 50 milisegundos y ya incluye manifiestos de Kubernetes con liveness y readiness probes preparados para autoscaling horizontal."*

---

## 6. Detener el Entorno

Para apagar los contenedores:
```bash
docker compose down
```

Para reiniciar desde cero borrando los datos de la base de datos:
```bash
docker compose down -v
```
