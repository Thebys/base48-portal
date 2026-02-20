# Base48 Member Portal - Specifikace

## Přehled

Členský portál pro hackerspace Base48. Go reimplementace původního Haskell portálu s Keycloak autentizací.

## Funkce

### Autentizace
- Keycloak OIDC SSO
- Service Account pro automatizaci
- Role: `memberportal_admin`, `active_member`, `in_debt`
- Dual client architektura (web + service account)

### Správa členů
- Profil uživatele (zobrazení, editace)
- Stav členství a plateb
- Admin: přehled uživatelů, správa rolí

### Platby
- FIO Bank automatická synchronizace
- Historie plateb a dlužných poplatků
- QR platební kódy
- Manuální přiřazení plateb (admin)
- Automatické generování měsíčních poplatků

### Fundraising
- Projekty s vlastním VS
- Sledování příspěvků na projekty

### Administrace
- Správa uživatelů a rolí
- Finanční přehled
- System logs (audit)
- Nastavení portálu

## Databázový model

```
levels          - Úrovně členství (Student, Full, Sponsor...)
users           - Členové hackerspace
payments        - Platby (FIO sync + manuální)
fees            - Měsíční poplatky
projects        - Fundraising projekty
system_logs     - Audit log
```

## Tech stack

- **Go 1.24** - Backend
- **Chi** - HTTP router
- **html/template** - Server-side rendering
- **SQLite** - Database (modernc.org/sqlite, pure Go)
- **sqlc** - Type-safe SQL
- **go-oidc** - Keycloak OIDC

## Struktura

```
cmd/
├── server/     # Hlavní aplikace
├── cron/       # portal-cron: sync, fees, report
├── import/     # Import ze staré databáze
└── test/       # Test skripty

internal/
├── auth/       # Keycloak OIDC + Service Account
├── config/     # Environment konfigurace
├── db/         # Database queries (sqlc)
├── email/      # Email client
├── fio/        # FIO Bank API
├── handler/    # HTTP handlery
├── keycloak/   # Keycloak Admin API
└── qrpay/      # QR platební kódy

web/templates/  # HTML templates
migrations/     # SQL schema
```

## API Endpoints

### Public
- `GET /` - Homepage

### Auth
- `GET /auth/login` - Keycloak login
- `GET /auth/callback` - OIDC callback
- `GET /auth/logout` - Logout

### Protected
- `GET/POST /profile` - Profil uživatele

### Admin UI
- `GET /admin/users` - Seznam uživatelů
- `GET /admin/users/{id}` - Detail uživatele
- `GET /admin/payments/unmatched` - Nespárované platby
- `GET /admin/projects` - Fundraising projekty
- `GET /admin/logs` - System logs
- `GET /admin/settings` - Nastavení

### Admin API
- `GET /api/admin/users` - Seznam uživatelů (JSON)
- `POST /api/admin/roles/assign` - Přiřazení role
- `POST /api/admin/roles/remove` - Odebrání role
- `POST /api/admin/payments/assign` - Přiřazení platby
- `POST /api/admin/payments/update` - Úprava platby
- `GET/POST/DELETE /api/admin/projects` - CRUD projekty

## Cron úlohy

Unified binary `portal-cron` se subcommandama:

- `portal-cron sync` - Synchronizace plateb z FIO + debt/role sync + pending emaily (každé 2 min)
- `portal-cron fees` - Generování měsíčních poplatků + debt warning emaily (1. den v měsíci)
- `portal-cron report` - Report nespárovaných plateb (ad-hoc)

## TODO

- [ ] Email notifikace (uvítání, upomínky)
- [ ] Level management (admin UI)
- [ ] Member state management (admin UI)
- [ ] CSRF protection
- [ ] Rate limiting

## Konfigurace

Viz `.env.example`:
- `PORT`, `BASE_URL` - Server
- `DATABASE_URL` - SQLite
- `KEYCLOAK_*` - OIDC + Service Account
- `BANK_FIO_TOKEN` - FIO API
- `SESSION_SECRET` - Sessions
