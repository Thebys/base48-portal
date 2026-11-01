# Base48 Member Portal - Specifikace

Go reimplementace původního Haskell portálu s Keycloak autentizací. Co portál
umí a jak se spouští, je v [README](README.md). Routy jsou v `cmd/server/main.go`,
konfigurace v `.env.example`, schéma v `migrations/`.

## Autentizace a role

- Přihlášení přes Keycloak OIDC, role se čtou z ID tokenu
- Service account pro cron a změny rolí přes Keycloak Admin API
- `memberportal_admin`: přístup do Správy
- `active_member`: každý člen ve stavu `accepted`
- `in_debt`: záporná bilance, cron ji přidává a odebírá každé 2 minuty
- Nastavení Keycloaku: [docs/KEYCLOAK_SETUP.md](docs/KEYCLOAK_SETUP.md)

## Členství a platby

- Stavy: `awaiting` (po prvním přihlášení), `accepted`, `suspended`, `exmember`, `rejected`,
  `external` (nečlen s účtem)
- Kdo smí co, rozhoduje `internal/access`: stav členství a navíc `access_grants`
  (oprávnění nezávislá na stavu, zatím autodílna)
- Nový člen dostane VS (`users.payments_id`) hned při prvním přihlášení
- Fees vznikají 1. v měsíci pro `accepted` členy: `level_actual_amount`, jinak částka úrovně
- Příchozí platby z FIO se párují podle VS na člena, jinak na fundraising projekt
- Odchozí platby se jen zaznamenají: ke členovi nikdy, k projektu, když nesou jeho VS,
  jinak je k projektu přiřadí admin. Členové u nich vidí datum, částku, projekt a
  `public_note` (předvyplněný komentářem z banky)
- K projektu patří platba jen přes `payments.project_id`; VS projektu rozhoduje jen o přiřazení
- Bilance = platby s VS člena − fees

## Tech stack

- **Go 1.26**, **Chi**, **html/template**
- **SQLite** (modernc.org/sqlite, pure Go), **sqlc**, migrace zabalené v binárce
- **go-oidc** (Keycloak), FIO API, SMTP

## Struktura

```
cmd/
├── server/     # Web server
├── cron/       # portal-cron: daemon, sync, fio-backfill, fio-balances, fees, debt-emails, bar-debt-emails, report
├── import/     # Import ze staré databáze
└── test/       # Ad-hoc test skripty

internal/
├── access/     # Kdo smí co: stav členství + oprávnění
├── auth/       # Keycloak OIDC + Service Account
├── bank/       # Platební účet (zaznamenaný FIO syncem)
├── config/     # Environment konfigurace
├── db/         # sqlc: queries.sql → queries.sql.go
├── email/      # Šablony, outbox, SMTP
├── fio/        # FIO Bank API
├── handler/    # HTTP handlery
├── keycloak/   # Keycloak Admin API
├── migrate/    # Migrace při startu serveru
├── money/      # Čtení částek zadaných v portálu
├── qrpay/      # QR platební kódy (SPAYD)
└── unmatched/  # Nespárované příchozí platby (admin i portal-cron report)

web/            # Šablony (vč. e-mailů) a statické soubory
migrations/     # SQL migrace
contrib/        # revbank-sync.sh pro bar kiosek
```

## Otevřené

- CSRF ochrana
- Rate limiting
