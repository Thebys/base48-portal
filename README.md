# Base48 Member Portal

Členský portál brněnského hackerspace Base48.

## Funkce

- Keycloak OIDC autentizace, role `active_member` a `in_debt` se syncují zpět do Keycloaku
- Automatické stahování a párování plateb z FIO Banky (podle VS)
- Automatická správa měsíčních členských příspěvků, člen si může zvýšit úroveň nebo částku
- QR platební kódy (v profilu i v upomínkách)
- E-maily přes outbox: registrace, přijetí, upomínky dluhu, dluh na baru; šablony cs/en se upravují v adminu
- Finanční přehled a fundraising projekty s vlastním VS: kolik se vybralo, utratilo a za co
- Rezervace stání v autodílně
- Bar: zůstatek a nákupy z RevBank kiosku
- Admin rozhraní pro správu uživatelů, plateb, úrovní, autodílny, baru, e-mailů a nastavení

## Rychlý start

```bash
make setup      # Závislosti + .env
$EDITOR .env    # Nastavení konfigurace
make run        # Spuštění serveru (DB a migrace si vytvoří sám)
```

Server běží na `http://localhost:4848` (`PORT` v `.env`).

## Požadavky

- Go 1.26+
- Keycloak server
- Volitelně: sqlc (`make tools`) pro změny SQL dotazů, SQLite3 CLI na ruční dotazy do DB

## Vývoj

```bash
make tools      # sqlc + air
make hooks      # Git hook proti commitu secrets (repo je veřejné)
make dev        # Hot reload (air)
make sqlc       # Přegenerování internal/db po změně queries.sql
make build-all  # Build všech binárků
make test       # Testy
make help       # Všechny příkazy
```

## Cron úlohy

```bash
portal-cron daemon   # Vše na jednom místě (sync á 2 min, poplatky a upomínky 1. v měsíci, barové dluhy denně od 10:00)
portal-cron sync     # Synchronizace plateb + role sync (každé 2 min)
portal-cron fees     # Měsíční poplatky (1. den v měsíci)
portal-cron debt-emails      # Upomínky na členské příspěvky za tento měsíc (-dry-run jen vypíše, kdo by co dostal)
portal-cron bar-debt-emails  # Upomínky dluhu na baru (zapíná se a nastavuje v Správa → E-maily → Upozornění)
portal-cron report   # Report nespárovaných plateb (ad-hoc)
```

## Nasazení

Produkce jede přes Ansible z privátního repa `base48/servers-config-ng`
(playbook potřebuje vault se secrets, tohle repo je veřejné):

```bash
cd <servers-config-ng>
ansible-playbook -i phoenix, member-portal/playbooks/deploy.yaml --ask-vault-pass
```

Ručně (dev, nebo fallback na hostu):

```bash
cp .env.example .env && $EDITOR .env
docker compose up -d --build
```

Portál pak poslouchá na `127.0.0.1:8090` (`PORTAL_BIND_*` v `.env`).
Stack je app-only — TLS a vhosty zůstávají na reverse proxy hostu.

- [Docker stack, orchestrace, vault, zálohy](docs/DEPLOYMENT_DOCKER.md)

## RevBank (bar kiosek)

Integrace s [RevBank](https://github.com/vega-d/revbank) kioskem.
Sync script: [`contrib/revbank-sync.sh`](contrib/revbank-sync.sh) — běží na kiosku via cron, pushuje data do portálu.

Detaily: [docs/REVBANK_INTEGRATION.md](docs/REVBANK_INTEGRATION.md)

## Dokumentace

- [Docker deployment, orchestrace a secrets](docs/DEPLOYMENT_DOCKER.md)
- [Keycloak setup](docs/KEYCLOAK_SETUP.md)
- [RevBank integrace](docs/REVBANK_INTEGRATION.md)
- [Frontend pravidla](FRONTEND.md)
- [Specifikace](SPEC.md)
