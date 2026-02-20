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
portal-cron sync     # Synchronizace plateb + role sync (každé 2 min)
portal-cron fees     # Měsíční poplatky (1. den v měsíci)
portal-cron report   # Report nespárovaných plateb (ad-hoc)
```

## Dokumentace

- [Keycloak setup](docs/KEYCLOAK_SETUP.md)
- [Specifikace](SPEC.md)
