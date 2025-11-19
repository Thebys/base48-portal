# Base48 Member Portal

Member portál pro hackerspace Base48 s Keycloak SSO autentizací.

**Status:** 🚧 Active Development - Fáze 3 (Admin features) dokončena

## Features

- ✅ Keycloak OIDC SSO autentizace (funguje!)
- ✅ Správa členských profilů s přehledem plateb a bilance
- ✅ Evidence plateb a poplatků (kompletní import + zobrazení)
- ✅ Flexibilní úrovně členství (12 úrovní)
- ✅ Admin rozhraní pro správu uživatelů a rolí
- ✅ Keycloak service account integrace pro automatizaci
- ✅ Import historických dat (152 users, 3,855 payments, 5,027 fees)
- ✅ Detailní přehled plateb v profilu uživatele
- ✅ Type-safe SQL (sqlc)
- ✅ Pure Go SQLite driver (bez CGO)
- ✅ Minimalistická architektura

## Quick Start

### Prerequisites

- Go 1.21+ (testováno na 1.24.0)
- Keycloak server s nakonfigurovaným realm a clientem
- (SQLite není potřeba - používá se pure Go driver)

### Setup

1. **Clone a příprava**
```bash
git clone <repo>
cd base48-portal
cp .env.example .env
```

2. **Edituj `.env`**
```bash
# Nastav Keycloak credentials
KEYCLOAK_URL=https://your-keycloak.com
KEYCLOAK_REALM=your-realm

# Web application client (pro přihlášení uživatelů)
KEYCLOAK_CLIENT_ID=member-portal
KEYCLOAK_CLIENT_SECRET=your-secret

# Service account client (pro automatizaci a admin operace)
KEYCLOAK_SERVICE_ACCOUNT_CLIENT_ID=member-portal-service
KEYCLOAK_SERVICE_ACCOUNT_CLIENT_SECRET=your-service-secret

# Vygeneruj session secret
SESSION_SECRET=$(openssl rand -base64 32)
```

3. **Inicializuj databázi**
```bash
mkdir -p data
# Windows (MSYS/Git Bash):
sqlite3 data/portal.db < migrations/001_initial_schema.sql
# Nebo použij DB browser nebo jiný SQL client
```

4. **Nainstaluj dependencies a vygeneruj SQL kód**
```bash
go mod tidy
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
sqlc generate
```

5. **Build a spusť server**
```bash
go build -o portal.exe cmd/server/main.go
./portal.exe
```

Server běží na `http://localhost:4848` (nebo PORT z .env)

## Data Import (from old rememberportal)

Pro import dat ze staré databáze:

```bash
# 1. Zkopíruj starou databázi do migrations/
cp /path/to/rememberportal.sqlite3 migrations/

# 2. Vytvoř zálohu současné databáze (pokud existuje)
cp data/portal.db data/portal.db.backup

# 3. Spusť import skript
sqlite3 data/portal.db < migrations/002_import_old_data.sql
```

**Co se importuje:**
- ✅ Všechny levels (úrovně členství) - 12 úrovní
- ✅ Všichni uživatelé s kompletními profily
- ✅ Všechny platby (payments) včetně FIO JSON dat
- ✅ Všechny poplatky (fees) - očekávané měsíční platby
- ✅ Historická data od roku 2010

**Automatické mapování:**
- Zachovává původní user ID pro konzistenci
- Mapuje vztahy user → payments → fees
- Orphaned payments (bez uživatele) se také importují
- `keycloak_id` je NULL - naváže se při prvním přihlášení

**Proces napojení Keycloak ID při prvním přihlášení:**
1. Uživatel se přihlásí přes Keycloak (email: `user@example.com`)
2. Systém ho nenajde podle Keycloak ID (je NULL)
3. Najde ho podle emailu v tabulce users
4. Automaticky naváže `keycloak_id` z OIDC tokenu
5. Příště už ho najde rovnou podle Keycloak ID

## Project Structure

```
base48-portal/
├── cmd/
│   ├── server/          # Main aplikace
│   ├── import/          # Import tool ze staré databáze
│   ├── cron/            # Plánované úlohy (např. update_debt_status)
│   └── test/            # Test skripty pro Keycloak integraci
├── internal/
│   ├── auth/            # Keycloak OIDC + service account
│   ├── config/          # Environment konfigurace
│   ├── db/              # Database queries (sqlc)
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

### Regenerate SQL code
```bash
sqlc generate
```

### Run with live reload
```bash
go install github.com/air-verse/air@latest
air
```

### Build for production
```bash
go build -o portal cmd/server/main.go
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
- **GET /admin/users** - Webové rozhraní pro správu uživatelů
  - Zobrazení všech uživatelů z DB
  - Keycloak status (enabled/disabled/not linked)
  - Aktuální role zobrazené jako badges
  - Inline přiřazování/odebírání rolí

API endpointy (JSON):
- **GET /api/admin/users** - Seznam všech uživatelů s Keycloak info
- **POST /api/admin/roles/assign** - Přiřadit roli uživateli
- **POST /api/admin/roles/remove** - Odebrat roli uživateli
- **GET /api/admin/users/roles** - Zobrazit role konkrétního uživatele

Podporované role pro správu:
- `active_member` - aktivní členství
- `in_debt` - dluh na účtu

## Automated Tasks (Cron)

Service account umožňuje automatizované úlohy bez přihlášeného uživatele:

```bash
# Příklad: Update debt status based on balance
go run cmd/cron/update_debt_status.go
```

Test skripty:
```bash
# Zobraz všechny uživatele v Keycloak
go run cmd/test/list_users.go

# Test přiřazení/odebrání role
TEST_USER_ID=<keycloak-user-id> go run cmd/test/test_role_assign.go
```

## TODO

- [ ] Manuální přiřazování plateb
- [ ] Import plateb z FIO API
- [ ] Email notifikace
- [ ] CSRF ochrana
- [ ] Rate limiting

## License

MIT

## Contributing

PRs welcome! Viz `SPEC.md` pro detaily o architektuře a principech.
