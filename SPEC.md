# Base48 Member Portal - Specifikace

Go reimplementace původního Haskell portálu s Keycloak autentizací. Co portál
umí a jak se spouští, je v [README](README.md). Routy jsou v `cmd/server/main.go`,
konfigurace v `.env.example`, schéma v `migrations/`.

## Autentizace a role

- Přihlášení přes Keycloak OIDC, role se čtou z ID tokenu
- Service account pro cron a změny rolí přes Keycloak Admin API
- `memberportal_admin`: přístup do Správy. Admin si v menu účtu může zapnout
  „Zobrazit jako člen“: do konce session nebo do „Zpět na admina“ portál roli
  nevidí (příznak v session, `auth.ViewAsMember`). Funguje to i na produkci,
  může to jen ubrat práva a další přihlášení začíná zase jako admin.
- `active_member`: každý člen ve stavu `accepted`
- `in_debt`: záporná bilance, cron ji přidává a odebírá každé 2 minuty
- Nastavení Keycloaku: [docs/KEYCLOAK_SETUP.md](docs/KEYCLOAK_SETUP.md)

## Členství a platby

- Stavy: `awaiting` (po prvním přihlášení), `accepted`, `suspended`, `exmember`, `rejected`,
  `external` (nečlen s účtem)
- Kdo smí co, rozhoduje `internal/access`: stav členství a navíc `access_grants`
  (oprávnění nezávislá na stavu, zatím autodílna)
- Nový člen dostane VS (`users.payments_id`) hned při prvním přihlášení
- Fees vznikají 1. v měsíci pro `accepted` členy: `level_actual_amount`, jinak částka úrovně.
  Hned po nich se zařadí upozornění na příspěvky (nejvýš jedno za kalendářní měsíc),
  odcházejí za tři dny v 10:00. Měsíc posledního běhu si démon pamatuje v `settings`
  (`membership_fees_month`, `membership_reminders_month`), takže restart běh nezopakuje
  a výpadek 1. dožene do 7. dne
- Kde člen stojí s příspěvkem, říká `internal/dues`: měsíční příspěvek, do kdy má zaplaceno,
  jaké upozornění mu patří a co doporučit zaplatit (dluh + N měsíců, výchozí 3). Upozornění:
  před koncem předplatného (nemá zaplacený další měsíc; zapíná admin), záporná bilance
  (dluží aspoň jeden příspěvek), upozornění na dluh (aspoň dva)
- E-maily spravuje `/admin/emails`: fronta, šablony (texty v `email_template_content`,
  výchozí v `internal/email`, seznam bloků v `internal/email/registry.go`) a upozornění
- Každý e-mail čeká ve frontě a těsně před odesláním (i ručním) dostane poslední kontrolu:
  upomínka se přepočítá nebo zruší, uvítání, registrace a pozastavení se zruší, když člen
  mezitím změnil stav. S `EMAIL_ENABLED=false` se nic nezařazuje ani neodesílá
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
