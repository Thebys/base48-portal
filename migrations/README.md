# Database Migrations

Tento adresář obsahuje SQL migrace pro Base48 Member Portal.

## Migrace

### 001_initial_schema.sql
Vytvoří základní databázové schema:
- **levels** - úrovně členství (Regular, Student, Support, atd.)
- **users** - členové s profily a Keycloak ID
- **payments** - skutečné platby (FIO bank transactions)
- **fees** - očekávané měsíční poplatky
- Indexy pro rychlé dotazy

**Použití:**
```bash
sqlite3 data/portal.db < migrations/001_initial_schema.sql
```

### 002_import_old_data.sql
Importuje data ze staré `rememberportal.sqlite3` databáze.

**Prerekvizity:**
- Stará databáze v `migrations/rememberportal.sqlite3`
- Vytvořená struktura pomocí `001_initial_schema.sql`

**Co importuje:**
- 12 membership levels
- ~150 users s kompletními profily
- ~3,800 payments od roku 2010
- ~5,000 fees (očekávané měsíční platby)

**Použití:**
```bash
# Vytvoř zálohu
cp data/portal.db data/portal.db.backup

# Spusť import
sqlite3 data/portal.db < migrations/002_import_old_data.sql
```

**Output:**
Skript vypíše shrnutí:
- Počet importovaných uživatelů
- Počet importovaných plateb
- Počet importovaných fees
- Rozsah dat
- Počet orphaned payments (bez uživatele)

## Automatické generování měsíčních poplatků

Po importu dat je potřeba spravovat měsíční poplatky nových období.

### Cron job: create_monthly_fees

**Účel:** Vytváří měsíční fee záznamy pro všechny aktivní členy (`state='accepted'`)

**Použití:**
```bash
# Jednoráz spuštění
go run cmd/cron/create_monthly_fees.go

# Nebo zkompilovaný binary
go build -o create_monthly_fees ./cmd/cron/create_monthly_fees.go
./create_monthly_fees
```

**Crontab nastavení** (1. den v měsíci):
```bash
0 0 1 * * cd /path/to/portal && ./create_monthly_fees >> logs/fees.log 2>&1
```

**Logika:**
- Vytvoří fee pro první den aktuálního měsíce
- Používá `level_actual_amount` (fallback na `level.amount`)
- Kontroluje duplicity - idempotentní (bezpečné opakované spuštění)
- Pouze pro členy se stavem `accepted`

**Příklad output:**
```
Creating fees for period: 2025-12
Processing 57 accepted members...
  ✓ Created fee for user@example.com: 1000 Kč (fee_id: 5028)
  ⊘ Skipping user2@example.com - fee already exists for 2025-12
  ✓ Created fee for user3@example.com: 600 Kč (fee_id: 5029)
  ✉ Sent debt warning email (balance: -3200 Kč)

Summary:
  Period: 2025-12
  Total users: 57
  Created: 56
  Skipped (already exists): 1
  Debt warning emails sent: 1
  Errors: 0
```

**Email notifikace:**
- Po vytvoření každé fee se automaticky zkontroluje balance člena
- Pokud balance < -(2× měsíční poplatek), pošle se **debt warning email**
- Emaily se posílají pouze pokud je SMTP nakonfigurováno
- Chyby při posílání emailů necrashnou celý job (graceful handling)

### 003_system_logs.sql
Unified logging pro všechny subsystémy (email, fio_sync, cron).

**Použití:**
```bash
sqlite3 data/portal.db < migrations/003_system_logs.sql
```

## Email Systém

**SMTP konfigurace** (`.env`):
```bash
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USERNAME=noreply@base48.cz
SMTP_PASSWORD=your-password
SMTP_FROM=Base48 <noreply@base48.cz>
```

**Scénáře:** Welcome, Negative Balance, Debt Warning (>2× fee), Membership Suspended

**Testování:** [MailHog](https://github.com/mailhog/MailHog) - `docker run -p 1025:1025 -p 8025:8025 mailhog/mailhog`

## Import dat ze staré databáze

Klíčové změny: `altcontact`→`alt_contact`, `state` lowercase, `keycloak_id` NULL (napojí se při prvním loginu)

**Automatické napojení Keycloak:** První login najde usera podle emailu a naváže `keycloak_id`

## Best Practices

- Vždy backup před migrací: `cp data/portal.db data/portal.db.backup`
- Kontroluj integrity: `sqlite3 data/portal.db "PRAGMA integrity_check;"`
