# Base48 Member Portal

Member portál pro hackerspace Base48 s Keycloak SSO autentizací.

**Status:** 🚧 Active Development - Fáze 1 (Základ) dokončena

## Features

- ✅ Keycloak OIDC SSO autentizace (funguje!)
- ✅ Správa členských profilů (základní UI)
- ✅ Evidence plateb a poplatků (DB schema připraveno)
- ✅ Flexibilní úrovně členství
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
KEYCLOAK_CLIENT_ID=member-portal
KEYCLOAK_CLIENT_SECRET=your-secret

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

# 2. Zkompiluj a spusť import tool
go build -o import.exe cmd/import/main.go
./import.exe
```

Import automaticky:
- Naimportuje všechny levels (úrovně členství)
- Naimportuje všechny uživatele s daty (email, jméno, telefon, stav, atd.)
- Přeskočí duplicitní emaily (OR IGNORE)
- Nastaví `keycloak_id` na NULL - bude napojen při prvním přihlášení

Když se importovaný uživatel poprvé přihlásí přes Keycloak:
1. Systém ho nenajde podle Keycloak ID (je NULL)
2. Najde ho podle emailu
3. Automaticky naváže Keycloak ID pomocí `LinkKeycloakID`
4. Příště už ho najde podle Keycloak ID

## Project Structure

```
base48-portal/
├── cmd/
│   ├── server/          # Main aplikace
│   └── import/          # Import tool ze staré databáze
├── internal/
│   ├── auth/            # Keycloak OIDC
│   ├── config/          # Environment konfigurace
│   ├── db/              # Database queries (sqlc)
│   └── handler/         # HTTP handlery
├── web/
│   ├── templates/       # HTML templates
│   └── static/          # CSS, JS, assets
├── migrations/          # SQL schema & migrations
├── sqlc.yaml            # sqlc konfigurace
└── SPEC.md              # Detailní specifikace
```

## Keycloak Setup

1. Vytvoř nový Client v Keycloak:
   - Client ID: `member-portal`
   - Client Protocol: `openid-connect`
   - Access Type: `confidential`
   - Valid Redirect URIs: `http://localhost:8080/auth/callback`

2. Zkopíruj Client Secret z tab "Credentials"

3. Nastav v `.env`:
   - `KEYCLOAK_CLIENT_ID`
   - `KEYCLOAK_CLIENT_SECRET`

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

## TODO

- [ ] Admin panel pro správu členů
- [ ] Manuální přiřazování plateb
- [ ] Import plateb z FIO API
- [ ] Email notifikace
- [ ] CSRF ochrana
- [ ] Rate limiting

## License

MIT

## Contributing

PRs welcome! Viz `SPEC.md` pro detaily o architektuře a principech.
