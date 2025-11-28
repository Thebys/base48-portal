# Base48 Member Portal

Member portál pro hackerspace Base48 s Keycloak SSO autentizací.

**Status:** 🚧 Active Development - Fáze 3 (Admin features) dokončena

## Features

- ✅ Keycloak OIDC SSO autentizace
- ✅ Správa členských profilů s přehledem plateb a bilance
- ✅ Evidence plateb a poplatků
- ✅ Flexibilní úrovně členství
- ✅ Admin rozhraní pro správu uživatelů a rolí (filtering, sorting)
- ✅ FIO Bank integrace - automatická synchronizace plateb
- ✅ Finanční přehled - správa nespárovaných příchozích plateb
- ✅ Keycloak service account integrace pro automatizaci
- ✅ Username synchronizace z Keycloak
- ✅ Email systém (welcome, debt warnings, member notifications)
- ✅ Automatizované měsíční poplatky s email notifikacemi
- ✅ Type-safe SQL (sqlc)
- ✅ Pure Go SQLite driver (bez CGO)
- 🔜 Keycloak-less mode je plánován

## Quick Start

### Prerequisites

- Go 1.21+ (testováno na 1.24.0)
- Keycloak server s nakonfigurovaným realm a clientem
- SQLite3 CLI (pro inicializaci DB)

### Setup & Run

```bash
# 1. Setup (dependencies + config)
make setup

# 2. Inicializuj databázi
make db-init

# 3. Edituj .env soubor
nano .env  # nebo tvůj editor

# 4. Vygeneruj SQL kód
make sqlc

# 5. Spusť server
make run         # jednorázové spuštění
make dev         # s hot reload (air)
```

Server běží na `http://localhost:4848` (nebo PORT z .env)

### Cross-platform Notes

**Linux/macOS:**
- Makefile příkazy fungují nativně
- Binary: `./portal`

**Windows:**
- Použij Git Bash nebo WSL pro Makefile
- Binary: `./portal.exe`
- Alternativa: `go run cmd/server/main.go`

### První přihlášení

Při prvním přihlášení existujícího uživatele přes Keycloak:
1. Systém najde uživatele podle emailu
2. Automaticky naváže `keycloak_id` z OIDC tokenu
3. Synchronizuje username z Keycloak `preferred_username`
4. Další přihlášení už probíhá přímo přes Keycloak ID

## Project Structure

```
base48-portal/
├── cmd/
│   ├── server/          # Main aplikace
│   ├── import/          # Import tool ze staré databáze
│   ├── cron/            # Plánované úlohy (sync_fio_payments, update_debt_status)
│   └── test/            # Test skripty pro Keycloak a FIO API
├── internal/
│   ├── auth/            # Keycloak OIDC + service account
│   ├── config/          # Environment konfigurace
│   ├── db/              # Database queries (sqlc)
│   ├── fio/             # FIO Bank API client
│   ├── handler/         # HTTP handlery
│   └── keycloak/        # Keycloak Admin API client
├── web/
│   ├── templates/       # HTML templates
│   └── static/          # CSS, JS, assets
├── migrations/          # SQL schema & migrations
├── docs/                # Dokumentace (Keycloak setup)
├── sqlc.yaml            # sqlc konfigurace
└── SPEC.md              # Detailní specifikace
```

## Keycloak Setup

Portál používá **dva Keycloak klienty**:
1. **Web client** - pro přihlášení uživatelů přes prohlížeč
2. **Service account client** - pro automatizaci (cron úlohy, admin operace)

### Web Application Client

1. Vytvoř nový Client v Keycloak:
   - Client ID: `member-portal`
   - Client Protocol: `openid-connect`
   - Access Type: `confidential`
   - Valid Redirect URIs: `http://localhost:4848/auth/callback`

2. Zkopíruj Client Secret z tab "Credentials"

### Service Account Client

1. Vytvoř další Client v Keycloak:
   - Client ID: `member-portal-service`
   - Client Protocol: `openid-connect`
   - Access Type: `confidential`
   - Service Accounts Enabled: `ON`

2. Zkopíruj Client Secret z tab "Credentials"

3. V tab "Service Account Roles", přiřaď:
   - **realm-management** → `view-users`, `manage-users`

### Nastavení rolí

V Keycloak vytvoř tyto **realm roles**:
- `active_member` - aktivní člen
- `in_debt` - člen s dluhem
- `memberportal_admin` - admin práva v portálu

Viz detaily v [`docs/KEYCLOAK_SETUP.md`](docs/KEYCLOAK_SETUP.md)

## Development

```bash
make dev          # Run s hot reload (air)
make sqlc         # Regenerate SQL code
make build        # Build aplikace
make build-all    # Build všech binárků (server + cron)
make test         # Spusť testy
make clean        # Vymaž build artifacts
make help         # Zobraz všechny dostupné příkazy
```

## Database Schema

- **levels** - Úrovně členství (Student, Regular, Sponsor...)
- **users** - Členové hackerspace
- **payments** - Evidence plateb
- **fees** - Měsíční poplatky

Detaily viz `migrations/001_initial_schema.sql`

## Tech Stack

- **Go 1.24** - Backend
- **Chi** - HTTP router
- **go-oidc** - Keycloak OIDC autentizace
- **sqlc** - Type-safe SQL code generation
- **modernc.org/sqlite** - Pure Go SQLite driver (bez CGO)
- **Tailwind CSS** - Styling (plánováno)
- **html/template** - Server-side rendering

## Admin Features

Po přihlášení jako admin (role `memberportal_admin`):

**Správa uživatelů** (`/admin/users`):
- Zobrazení všech uživatelů s Keycloak statusem a rolemi
- Filtering: state, Keycloak status, balance, search
- Sorting: ID, balance (ascending/descending)
- Inline správa rolí (assign/remove)

**Finanční přehled** (`/admin/payments/unmatched`):
- Přehled nespárovaných příchozích plateb z FIO
- Kategorizace: prázdný VS, neznámý VS, sync chyby
- Collapsible sekce pro lepší přehlednost
- Statistiky a celkové částky

**API endpointy**:
- `GET /api/admin/users` - Seznam uživatelů
- `POST /api/admin/roles/assign` - Přiřadit roli
- `POST /api/admin/roles/remove` - Odebrat roli

## Automated Tasks (Cron)

Service account umožňuje automatizované úlohy bez přihlášeného uživatele:

```bash
# Build cron jobs
make build-all

# Synchronizace FIO plateb (doporučeno spouštět denně)
./sync_fio_payments

# Aktualizace dluhového statusu
./update_debt_status

# Test skripty
go run cmd/test/test_fio_api.go
go run cmd/test/list_users.go
TEST_USER_ID=<keycloak-user-id> go run cmd/test/test_role_assign.go
```

---

Více informací viz `SPEC.md` pro detaily o architektuře a principech.
