# Base48 Member Portal - Specifikace

## Přehled projektu

Member portál pro hackerspace Base48. Reimplementace původního Haskell portálu v Go s moderní autentizací přes Keycloak.

## Scope - CO DĚLÁME ✅

### Core Features (MVP)

1. **Autentizace & Autorizace**
   - Keycloak OIDC SSO integrace
   - Role: member, council, staff (admin)
   - Session management

2. **Správa členů**
   - Zobrazení vlastního profilu
   - Editace kontaktních údajů
   - Zobrazení stavu členství a plateb
   - Staff: správa všech členů

3. **Evidence plateb**
   - Zobrazení historie plateb
   - Zobrazení dlužných poplatků
   - Staff: manuální přiřazení plateb

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

1. **Automatická synchronizace s bankou** - pouze manuální import (zatím)
2. **Email notifikace** - bez SMTP integrace v MVP
3. **Komplexní reporty** - pouze základní přehledy
4. **API pro externí aplikace** - pouze interní UI
5. **Bitcoin platby** - pouze fiat
6. **Audit log** - RawData v Payment stačí
7. **Multi-tenancy** - pouze Base48

## Technický stack

- **Jazyk:** Go 1.24
- **Web framework:** Chi router (lehký, idiomatický)
- **Templates:** html/template (stdlib, simple)
- **CSS:** Tailwind CSS (via CDN, utility-first)
- **Databáze:** SQLite (modernc.org/sqlite - pure Go, bez CGO)
- **ORM:** sqlc (type-safe SQL, žádná magie)
- **Auth:** go-oidc (Keycloak OIDC)
- **Session:** gorilla/sessions
- **Config:** kelseyhightower/envconfig

## Architektura

```
base48-portal/
├── cmd/
│   ├── server/          # Main aplikace
│   └── import/          # Import tool ze staré databáze (rememberportal)
├── internal/
│   ├── config/          # Konfigurace (envconfig)
│   ├── auth/            # Keycloak OIDC
│   ├── db/              # Database layer (sqlc generated)
│   └── handler/         # HTTP handlery
├── web/
│   ├── templates/       # html/template soubory
│   │   ├── layout.html  # Shared layout
│   │   ├── home.html
│   │   ├── dashboard.html
│   │   └── profile.html
│   └── static/          # (budoucí) CSS, JS, assets
├── migrations/          # SQL migrace
│   ├── 001_initial_schema.sql
│   ├── 002_allow_null_keycloak_id.sql
│   ├── 002_import_old_data.sql
│   └── rememberportal.sqlite3 (gitignored)
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

### Fáze 2: Core features (ČÁSTEČNĚ DOKONČENO)
- [x] User profile view/edit
- [x] Payment history view (v dashboardu)
- [x] Fee overview (v dashboardu)
- [ ] Member listing (staff only)
- [ ] Payment balance calculation improvements

### Fáze 3: Admin features
- [ ] Member state management
- [ ] Manual payment assignment
- [ ] Level management

### Fáze 4: Polish
- [ ] Error handling
- [ ] Input validation
- [ ] Security hardening
- [ ] Documentation

## Konfigurace (env variables)

```bash
# Server
PORT=4848
BASE_URL=http://localhost:4848

# Database
DATABASE_URL=file:./data/portal.db?_fk=1
# SQLite s foreign key constraints enabled

# Keycloak
KEYCLOAK_URL=https://sso.base48.cz
KEYCLOAK_REALM=master
KEYCLOAK_CLIENT_ID=go-member-portal-dev
KEYCLOAK_CLIENT_SECRET=your-secret-here

# Session
SESSION_SECRET=generate-with-openssl-rand-base64-32
```

## Data Import

Pro import ze staré rememberportal databáze:

```bash
# 1. Zkopíruj starou databázi
cp /path/to/rememberportal.sqlite3 migrations/

# 2. Spusť import
go build -o import.exe cmd/import/main.go
./import.exe
```

Import automaticky:
- Naimportuje všechny membership levels (12 úrovní)
- Naimportuje všechny uživatele (152 users)
- Nastaví keycloak_id na NULL
- Při prvním přihlášení se keycloak_id automaticky linkuje

## Security considerations

- CSRF protection na všech POST/PUT/DELETE
- Secure session cookies (HttpOnly, Secure, SameSite)
- Input sanitization
- SQL injection prevention (sqlc)
- XSS prevention (templ auto-escaping)
- Rate limiting (optional)

## Implementované Features

### ✅ Authentication & Authorization
- Keycloak OIDC SSO integrace
- Session management (gorilla/sessions)
- Auto-registration nových uživatelů
- Auto-linking importovaných uživatelů

### ✅ User Management
- Dashboard s přehledem členství
- Profile edit (realname, phone, alt_contact)
- Zobrazení stavu členství (accepted/awaiting/suspended/exmember/rejected)
- Zobrazení úrovně členství a částky

### ✅ Payment & Fee Display
- Historie plateb (datum, částka, zdroj)
- Přehled poplatků (období, částka)
- Výpočet balance (payments - fees)
- Barevné indikátory (zelená/červená pro přeplatek/dluh)

### ✅ Data Migration
- Import tool pro migraci ze staré databáze
- 152 uživatelů naimportováno
- 12 membership levels
- Zachování všech dat (state, level, payments_id, atd.)

### 🚧 TODO
- Member listing (staff only)
- Manual payment assignment (staff)
- Level management (staff)
- Payment import z FIO API
- Email notifikace

---

**Verze:** 0.2.0-alpha
**Datum:** 2025-11-16
**Autor:** Base48 team
**Status:** Funkční prototyp s importovanými daty
