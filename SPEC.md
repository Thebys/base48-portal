# Base48 Member Portal - Specifikace

## Přehled projektu

Member portál pro hackerspace Base48. Reimplementace původního Haskell portálu v Go s moderní autentizací přes Keycloak.

## Scope - CO DĚLÁME ✅

### Core Features (MVP)

1. **Autentizace & Autorizace**
   - Keycloak OIDC SSO integrace (uživatelské přihlášení)
   - Keycloak Service Account (automatizace a admin operace)
   - Role: `memberportal_admin`, `active_member`, `in_debt`
   - Session management (pouze user info, ne tokeny)
   - Dual client architektura (web + service account)

2. **Správa členů**
   - Zobrazení vlastního profilu
   - Editace kontaktních údajů
   - Zobrazení stavu členství a plateb
   - Admin: přehled všech uživatelů (/admin/users)
   - Admin: správa Keycloak rolí (assign/remove)
   - Admin API pro programový přístup

3. **Evidence plateb**
   - Zobrazení historie plateb
   - Zobrazení dlužných poplatků
   - FIO Bank automatická synchronizace
   - Staff: manuální přiřazení plateb
   - Admin: finanční přehled nespárovaných plateb

4. **Úrovně členství**
   - Různé typy členství (Student, Full, Sponsor...)
   - Flexibilní poplatky (možnost platit více)

5. **Základní UI**
   - Server-side rendered (Go templates / templ)
   - Bootstrap 5 nebo Tailwind CSS
   - Responsive design

### Databázový model

```
Level (úrovně členství)
├── ID
├── Name (string, unique)
├── Amount (decimal) - měsíční poplatek
└── Active (bool)

User (členové)
├── ID
├── KeycloakID (string, unique, nullable) - propojení s Keycloak, NULL pro importované uživatele
├── Email (string, unique)
├── Realname (string, optional)
├── Phone (string, optional)
├── AltContact (string, optional)
├── LevelID (foreign key -> Level)
├── LevelActualAmount (decimal) - pro flexibilní poplatky
├── PaymentsID (string, optional, unique) - variabilní symbol
├── DateJoined (timestamp)
├── KeysGranted (timestamp, optional)
├── KeysReturned (timestamp, optional)
├── State (enum: awaiting, accepted, rejected, exmember, suspended)
├── IsCouncil (bool)
├── IsStaff (bool)
├── CreatedAt (timestamp)
└── UpdatedAt (timestamp)

Payment (platby)
├── ID
├── UserID (foreign key -> User, optional)
├── Date (timestamp)
├── Amount (decimal)
├── Kind (string) - typ zdroje (fio, manual, etc.)
├── KindID (string) - unique ID v rámci Kind
├── LocalAccount (string)
├── RemoteAccount (string)
├── Identification (string) - variabilní symbol
├── RawData (jsonb) - originální data
└── StaffComment (string, optional)

Fee (očekávané poplatky)
├── ID
├── UserID (foreign key -> User)
├── LevelID (foreign key -> Level)
├── PeriodStart (date) - první den měsíce
└── Amount (decimal)

UNIQUE CONSTRAINTS:
- Level: Name
- User: KeycloakID (nullable), Email, PaymentsID (nullable)
- Payment: (Kind, KindID)

NOTES:
- KeycloakID je nullable - umožňuje import uživatelů ze staré databáze
- Při prvním přihlášení přes Keycloak se automaticky linkuje pomocí LinkKeycloakID query
- Partial index na keycloak_id WHERE keycloak_id IS NOT NULL pro výkon
```

## Scope - CO NEDĚLÁME ❌

1. **Email notifikace** - bez SMTP integrace v MVP
2. **Komplexní reporty** - pouze základní přehledy
3. **API pro externí aplikace** - pouze interní UI
4. **Bitcoin platby** - pouze fiat
5. **Audit log** - RawData v Payment stačí
6. **Multi-tenancy** - pouze Base48

## Technický stack

- **Go 1.24** - Backend (cross-platform, pure Go)
- **Chi** - HTTP router
- **html/template** - Server-side rendering
- **Tailwind CSS** - Styling (via CDN)
- **SQLite** - Database (modernc.org/sqlite - pure Go, bez CGO)
- **sqlc** - Type-safe SQL
- **go-oidc** - Keycloak OIDC
- **gorilla/sessions** - Session management
- **Makefile** - Build automation (Linux/macOS/Windows Git Bash)

## Architektura

```
base48-portal/
├── cmd/
│   ├── server/          # Main aplikace
│   ├── import/          # Import tool ze staré databáze (rememberportal)
│   ├── cron/            # Automatizované úlohy (sync_fio_payments, update_debt_status)
│   └── test/            # Test skripty (test_fio_api, list_users, test_role_assign)
├── internal/
│   ├── config/          # Konfigurace (envconfig)
│   ├── auth/            # Keycloak OIDC + Service Account
│   │   ├── auth.go              # User authentication
│   │   └── service_account.go   # Service account client
│   ├── db/              # Database layer (sqlc generated)
│   ├── fio/             # FIO Bank API client
│   │   └── client.go            # Transaction fetching
│   ├── keycloak/        # Keycloak Admin API client
│   │   └── client.go            # Role management methods
│   └── handler/         # HTTP handlery
│       ├── handler.go           # Base handler
│       ├── dashboard.go         # User dashboard
│       ├── profile.go           # Profile edit
│       ├── admin.go             # Admin API endpoints
│       ├── admin_users.go       # Admin user management UI
│       └── admin_payments.go    # Admin financial overview
├── web/
│   ├── templates/       # html/template soubory
│   │   ├── layout.html                   # Shared layout
│   │   ├── home.html
│   │   ├── dashboard.html
│   │   ├── profile.html
│   │   ├── admin_users.html              # Admin user management
│   │   └── admin_payments_unmatched.html # Admin financial overview
│   └── static/          # (budoucí) CSS, JS, assets
├── migrations/          # SQL migrace
│   ├── 001_initial_schema.sql
│   ├── 002_allow_null_keycloak_id.sql
│   ├── 002_import_old_data.sql
│   └── rememberportal.sqlite3 (gitignored)
├── docs/                # Dokumentace
│   └── KEYCLOAK_SETUP.md        # Keycloak setup guide
├── data/                # SQLite databáze (gitignored)
├── sqlc.yaml            # sqlc konfigurace
├── go.mod
├── go.sum
├── SPEC.md
└── README.md
```

## Principy

1. **DRY** - žádná duplikace, sdílené komponenty
2. **Explicitní > Implicitní** - žádná magie, čitelný kód
3. **Type-safe** - sqlc pro DB, html/template pro UI
4. **Minimální dependencies** - pouze to co potřebujeme
5. **Easy to deploy** - single binary + static files
6. **Pure Go** - žádný CGO, běží všude (modernc.org/sqlite)

## Fáze implementace

### Fáze 1: Základ ✅ DOKONČENO (2025-11-16)
- [x] Projektová struktura
- [x] DB schema + migrace (SQLite s pure Go driverem)
- [x] sqlc setup (vygenerováno)
- [x] Keycloak auth flow (funguje s sso.base48.cz)
- [x] Základní server setup
- [x] Authentication middleware
- [x] Session management
- [x] Template rendering (html/template s layout pattern)
- [x] Auto-registration při prvním přihlášení
- [x] Import tool ze staré rememberportal databáze
- [x] Automatické linkování Keycloak ID pro importované uživatele
- [x] Dashboard s přehledem členství, plateb a poplatků
- [x] Profile view/edit (realname, phone, alt_contact)

### Fáze 2: Core features ✅ DOKONČENO (2025-11-17)
- [x] User profile view/edit
- [x] Payment history view (v dashboardu)
- [x] Fee overview (v dashboardu)
- [x] Member listing (admin only - /admin/users)
- [x] Payment balance calculation improvements

### Fáze 3: Admin features + Payment details ✅ DOKONČENO (2025-11-21)
- [x] Keycloak service account integration
- [x] Admin user management UI (/admin/users)
- [x] Role management (assign/remove via Admin API)
- [x] Admin API endpoints (JSON)
- [x] Automated tasks support (cron mode)
- [x] Import plateb a fees ze staré databáze (002_import_old_data.sql)
- [x] Detailní přehled plateb v profilu uživatele
- [x] Zobrazení členských příspěvků (fees) v profilu
- [x] Kalkulace a zobrazení celkově zaplacené částky
- [x] Vizuální indikace bilance (zelená/červená)
- [x] FIO Bank API integrace
- [x] Automatická synchronizace plateb z FIO (cron job)
- [x] Admin finanční přehled nespárovaných plateb
- [x] VS mapping na payments_id (ne user.id)
- [ ] Member state management (DB level)
- [ ] Manual payment assignment
- [ ] Level management

### Fáze 4: Polish
- [ ] Error handling
- [ ] Input validation
- [ ] Security hardening
- [ ] Documentation

## Konfigurace

Viz `.env.example` pro všechny potřebné environment variables. Klíčové:

- `PORT`, `BASE_URL` - Server config
- `DATABASE_URL` - SQLite path s FK constraints
- `KEYCLOAK_*` - Dual client config (web + service account)
- `BANK_FIO_TOKEN` - FIO Bank API
- `SESSION_SECRET` - Session encryption

## Data Import

```bash
# Build import tool
make build-all

# Import ze staré databáze
./import
```

Importuje 152 users, 3855 payments, 5027 fees, 12 levels. Keycloak ID se linkuje při prvním přihlášení.

## Security considerations

- CSRF protection na všech POST/PUT/DELETE
- Secure session cookies (HttpOnly, Secure, SameSite)
- Input sanitization
- SQL injection prevention (sqlc)
- XSS prevention (templ auto-escaping)
- Rate limiting (optional)

## Implementované Features

### ✅ Authentication & Authorization
- Keycloak OIDC SSO integrace (uživatelské přihlášení)
- Keycloak Service Account (automatizace bez uživatele)
- Dual client architecture (web + service account)
- Session management (gorilla/sessions, bez token storage)
- Auto-registration nových uživatelů
- Auto-linking importovaných uživatelů
- Role-based access control (`memberportal_admin`)

### ✅ User Management
- Dashboard s přehledem členství
- Profile edit (realname, phone, alt_contact)
- Zobrazení stavu členství (accepted/awaiting/suspended/exmember/rejected)
- Zobrazení úrovně členství a částky
- Admin: přehled všech uživatelů (/admin/users)
- Admin: Keycloak status (enabled/disabled/not linked)
- Admin: zobrazení a správa rolí

### ✅ Payment & Fee Display
- Historie plateb v profilu (datum, částka, VS, účet)
- Přehled členských příspěvků/fees (období, částka)
- Výpočet balance (payments - fees)
- Celková zaplacená částka + počet plateb
- Členem od (datum registrace)
- Barevné indikátory (zelená/červená pro bilanci, modrá pro total paid)

### ✅ Data Migration
- Import skript (002_import_old_data.sql)
- 152 users, 3,855 payments, 5,027 fees, 12 levels
- Zachování všech dat včetně historie od 2010
- Automatické linkování při prvním přihlášení

### ✅ Admin & Automation
- Admin UI pro správu uživatelů (/admin/users)
- Admin API endpointy (JSON):
  - GET /api/admin/users
  - POST /api/admin/roles/assign
  - POST /api/admin/roles/remove
  - GET /api/admin/users/roles
- Role whitelist security (`active_member`, `in_debt`)
- Keycloak Admin API client (internal/keycloak/client.go)
- Service account authentication
- Test skripty (cmd/test/)
- Cron mode examples (cmd/cron/update_debt_status.go)

### 🚧 TODO
- Manual payment assignment (admin)
- Level management (admin)
- Member state management (DB updates via admin)
- Payment import z FIO API
- Email notifikace

## Security Features

### ✅ Implementováno
- **Session Security**: HttpOnly, Secure (HTTPS only), SameSite cookies
- **No Token Leakage**: Tokeny nejsou uloženy v session ani odeslány klientovi
- **Role Whitelist**: Admin může spravovat pouze `active_member` a `in_debt` role
- **Authorization Middleware**: Double-check (RequireAuth + RequireAdmin)
- **Service Account Isolation**: Service account token oddělen od user session
- **SQL Injection Prevention**: sqlc type-safe queries

### 🚧 TODO
- CSRF protection
- Rate limiting
- Input sanitization/validation
- Audit logging

---

**Verze:** 0.4.0-alpha
**Datum:** 2025-11-19
**Autor:** Base48 team
**Status:** Funkční prototyp s kompletní platební historií a admin rozhraním
