# Base48 Member Portal

Členský portál brněnského hackerspace Base48.

## Funkce

- Keycloak OIDC autentizace
- Automatické stahování a párování plateb z FIO Banky
- Automatická správa měsíčních členských příspěvků
- QR platební kódy
- Fundraising projekty
- Admin rozhraní pro správu uživatelů, plateb a fundraisingu

## Rychlý start

```bash
make setup      # Závislosti + .env
make db-init    # Inicializace DB
nano .env       # Nastavení konfigurace
make sqlc       # Generování SQL kódu
make run        # Spuštění serveru
```

Server běží na `http://localhost:4848`

## Požadavky

- Go 1.24+
- Keycloak server
- SQLite3 CLI

## Vývoj

```bash
make dev        # Hot reload (air)
make build-all  # Build všech binárků
make test       # Testy
make help       # Všechny příkazy
```

## Cron úlohy

```bash
portal-cron daemon   # Vše na jednom místě (sync á 2 min, poplatky 1. v měsíci)
portal-cron sync     # Synchronizace plateb + role sync (každé 2 min)
portal-cron fees     # Měsíční poplatky (1. den v měsíci)
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
- [Specifikace](SPEC.md)
