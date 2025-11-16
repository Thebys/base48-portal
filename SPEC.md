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
├── KeycloakID (string, unique) - propojení s Keycloak
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
└── IsStaff (bool)

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
- User: KeycloakID, Email, PaymentsID (nullable)
- Payment: (Kind, KindID)
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

- **Jazyk:** Go 1.21+
- **Web framework:** Chi router (lehký, idiomatický)
- **Templates:** templ (type-safe, fast)
- **CSS:** Tailwind CSS (utility-first, minimální)
- **Databáze:** SQLite (jednoduché) nebo PostgreSQL (produkce)
- **ORM:** sqlc (type-safe SQL, žádná magie)
- **Auth:** go-oidc (Keycloak OIDC)
- **Session:** gorilla/sessions
- **Decimal:** shopspring/decimal (přesná aritmetika)

## Architektura

```
base48-portal/
├── cmd/
│   └── server/          # Main aplikace
├── internal/
│   ├── config/          # Konfigurace
│   ├── auth/            # Keycloak OIDC
│   ├── db/              # Database layer (sqlc generated)
│   ├── handler/         # HTTP handlery
│   ├── middleware/      # Auth middleware
│   ├── model/           # Domain modely
│   └── service/         # Business logika
├── web/
│   ├── templates/       # templ komponenty
│   └── static/          # CSS, JS, assets
├── migrations/          # SQL migrace
├── sqlc.yaml            # sqlc konfigurace
├── go.mod
├── go.sum
└── README.md
```

## Principy

1. **DRY** - žádná duplikace, sdílené komponenty
2. **Explicitní > Implicitní** - žádná magie, čitelný kód
3. **Type-safe** - sqlc pro DB, templ pro templates
4. **Minimální dependencies** - pouze to co potřebujeme
5. **Easy to deploy** - single binary + static files

## Fáze implementace

### Fáze 1: Základ ✅ DOKONČENO (2025-11-16)
- [x] Projektová struktura
- [x] DB schema + migrace (SQLite s pure Go driverem)
- [x] sqlc setup (vygenerováno)
- [x] Keycloak auth flow (funguje s sso.base48.cz)
- [x] Základní server setup
- [x] Authentication middleware
- [x] Session management

### Fáze 2: Core features
- [ ] User profile view/edit
- [ ] Member listing (staff only)
- [ ] Payment history view
- [ ] Fee overview

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
PORT=8080
BASE_URL=http://localhost:8080

# Database
DATABASE_URL=sqlite:///data/portal.db
# nebo: postgres://user:pass@localhost/base48

# Keycloak
KEYCLOAK_URL=https://auth.base48.cz
KEYCLOAK_REALM=base48
KEYCLOAK_CLIENT_ID=member-portal
KEYCLOAK_CLIENT_SECRET=xxx

# Session
SESSION_SECRET=random-32-bytes-here
```

## Security considerations

- CSRF protection na všech POST/PUT/DELETE
- Secure session cookies (HttpOnly, Secure, SameSite)
- Input sanitization
- SQL injection prevention (sqlc)
- XSS prevention (templ auto-escaping)
- Rate limiting (optional)

---

**Verze:** 0.1.0-draft
**Datum:** 2025-01-XX
**Autor:** Base48 team
