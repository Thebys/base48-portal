# Base48 Member Portal

Členský portál brněnského hackerspace Base48.

## File Hierarchy

├── **tasks** -- Routines to work with the project<br/>
&nbsp;|&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;├── **docs** -- Tasks related to the project documentation<br/>
&nbsp;|&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;|&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;&nbsp;├── **tree** -- Task used to generate this file hierarchy output<br/>

*This file hierarchy output is generated using the `tree` task that processes directories with the `.about` file containing short description about the purpose of the directory*

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
./sync_fio_payments    # Synchronizace plateb z FIO
./update_debt_status   # Aktualizace dluhů
```

## Dokumentace

- [Keycloak setup](docs/KEYCLOAK_SETUP.md)
- [Specifikace](SPEC.md)
