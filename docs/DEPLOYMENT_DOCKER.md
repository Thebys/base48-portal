# Deployment: Docker / Compose

Nasazení member portálu přes `docker compose`. **Tohle je jediný podporovaný
způsob nasazení** — Nix balení (`default.nix`) bylo z repa odstraněno.

Tenhle dokument popisuje **stack samotný**. Orchestraci a secrets řeší Ansible
z privátního repa — viz [Orchestrace a secrets](#orchestrace-a-secrets). Ruční
postup níž je použitelný pro dev a jako fallback, když je potřeba sáhnout na
hosta přímo.

## Architektura

Stack je **app-only** — neterminuje TLS a neřeší vhosty. To zůstává na reverse
proxy, kterou už host má.

```
internet :443
    │
    └─ nginx stream (SNI)             /etc/nginx/stream-sni.d/*.map
           │
           └─ nginx vhost             TLS termination, ACME
                   │
                   └─ proxy_pass ──► 127.0.0.1:8090
                                          │
                                          └─► kontejner portal :8080
                                                  │
                                                  ├─ /app/data/portal.db  (bind mount)
                                                  └─ /app/web             (static + templates)

                                              kontejner cron
                                                  └─ /app/data/portal.db  (stejný soubor)
```

Dvě služby, **jeden image**, liší se jen příkazem:

| služba   | proces                    | co dělá                                            |
|----------|---------------------------|----------------------------------------------------|
| `portal` | `/app/portal`             | HTTP server, **pouští migrace při startu**          |
| `cron`   | `/app/portal-cron daemon` | FIO sync á 2 min, poplatky 1. v měsíci, maily, barové upomínky denně |

`cron` startuje až když je `portal` **healthy** — migrace musí proběhnout dřív,
než na databázi sáhne daemon.

## Požadavky

- Docker Engine 24+ s Compose v2 (`docker compose`, ne `docker-compose`)
- ~200 MB místa na image, plus velikost databáze
- Reverse proxy na hostu (nginx, Caddy, Traefik — cokoliv)

Image je `alpine:3.21` + dvě statické Go binárky, **~61 MB**. Build context je
díky allowlistu v `.dockerignore` **~1,6 MB**.

## Kde image vzniká

**Produkce se nestaví na hostu — pullne hotový tag z GHCR.** Na produkčním
stroji tak není zdroják, Go toolchain ani build cache, a „co běží" je
jednoznačně dané tagem.

Publikuje se pushnutím verzovaného tagu; workflow `.github/workflows/release.yml`
pustí testy a teprve pak buildne a pushne:

```bash
# bump verze
$EDITOR VERSION                       # a PORTAL_VERSION v .env.example
git commit -am "Release 1.4.4"
git tag v1.4.4 && git push origin v1.4.4
```

Ruční fallback bez CI (potřebuje `docker login ghcr.io` s PAT se scope
`write:packages`):

```bash
make image-push
```

Balíček je **veřejný**, takže produkční host pullne bez přihlášení — na hostu
tedy neleží žádný registry credential. První publikace je ale v GHCR defaultně
**privátní**; po prvním pushi je potřeba viditelnost ručně přepnout.

> **Do image nikdy neposílej secret přes `--build-arg`.** Build args se
> natrvalo zapisují do `docker history` a u veřejného balíčku je uvidí každý.
> Secrets do kontejneru vstupují až za běhu přes `.env`.

Tagy jsou verze a commit sha. Záměrně **žádný `latest`** — produkce pinuje
konkrétní tag, takže rollback je změna jednoho řádku.

## Rychlý start (lokální vývoj)

Lokálně se staví ze zdrojáku; produkce pullne — viz sekce výš.

```bash
cp .env.example .env
$EDITOR .env                 # vyplnit secrets + PORTAL_* proměnné
mkdir -p data
docker compose up -d --build
docker compose ps            # obě služby, portal musí být (healthy)
curl -s localhost:8090/healthz
```

## Konfigurace

Všechno je v jediném `.env`. Ten soubor slouží **dvěma účelům** naráz:

1. `env_file:` — proměnné předané dovnitř kontejnerů (secrets, Keycloak, SMTP…)
2. `${...}` interpolace v `docker-compose.yml` (proměnné `PORTAL_*`)

Aplikační proměnné jsou popsané v `.env.example`. Navíc pro Docker:

| proměnná             | default        | význam                                                     |
|----------------------|----------------|------------------------------------------------------------|
| `PORTAL_BIND_ADDR`   | `127.0.0.1`    | Adresa, na které portál poslouchá. **Nechat na loopbacku.** |
| `PORTAL_BIND_PORT`   | `8090`         | Port pro reverse proxy                                      |
| `PORTAL_DATA_DIR`    | `./data`       | Adresář na hostu s `portal.db`                              |
| `PORTAL_UID` / `_GID`| `10001`        | UID:GID kontejneru — **musí vlastnit `PORTAL_DATA_DIR`**. Na produkci připnuté na `990:985`. |
| `PORTAL_TAG`         | `local`        | Tag image                                                   |
| `PORTAL_VERSION`     | —              | Verze v patičce portálu (drž synchronně se souborem `VERSION`) |
| `TZ`                 | `Europe/Prague`| Časová zóna — ovlivňuje výpočet období poplatků             |

Tři proměnné compose **přebíjí natvrdo**, protože `.env` je sdílený s bare-metal
během: `PORT=8080`, `DATABASE_URL=file:/app/data/portal.db?_pragma=busy_timeout(5000)`,
`WEB_ROOT=/app/web`. Uvnitř kontejneru je port vždycky 8080; ven se publikuje
`PORTAL_BIND_PORT`.

> **Pozor na `$` v hodnotách.** Compose je v `.env` interpretuje jako
> interpolaci. `SESSION_SECRET` generuj bez dolaru:
> `openssl rand -hex 32`.

### Oprávnění k datovému adresáři

Databáze je bind-mountnutý soubor, aby se dala zálohovat obyčejným `cp`/`scp`.
Kontejner tedy musí běžet pod UID, které na něj má právo zápisu.

**Na produkci je UID:GID připnuté na `990:985`** (`portal_uid` / `portal_gid`
v playbooku). Playbook uživatele zakládá s těmito čísly, nenechává si je
přidělit. Důvod je stěhování mezi stroji: číselné vlastnictví souboru musí
znamenat totéž na každém stroji, kam se databáze zkopíruje, jinak tam skončí
jako nečitelná pro kontejner.

```bash
# ověření na hostu
stat -c '%u:%g' /var/lib/member-portal     # → 990:985

# lokálně stačí vlastní účet
id -u; id -g                               # → 1000:1000
```

> Když se databáze kopíruje ručně, přenášej ji s `rsync -a` / `scp -p` a po
> rozbalení zkontroluj `stat`. `chown 990:985` je levnější než hledat, proč
> portál hlásí `unable to open database file`.

## Orchestrace a secrets

Produkci nasazuje Ansible z **privátního** repa `base48/servers-config-ng`,
protože playbook potřebuje vault se secrets a `base48-portal` je veřejné.

```bash
cd <servers-config-ng>
ansible-playbook -i phoenix, member-portal/playbooks/deploy.yaml --ask-vault-pass
```

Playbook zazálohuje databázi, nainstaluje `compose.yaml`, vygeneruje `.env`,
pullne image, překlopí stack, spravuje nginx vhost i SNI fragment a nakonec
ověří `/healthz` i reálný request přes SNI vrstvu. Verze image je připnutá
v jeho `vars:` jako `portal_version` — **bump téhle hodnoty je celý release**.

> Playbook v tomhle repu **není**. Byl tu do 1.7.0 v `ansible/`, ale nikdy
> neběžel proti produkci, mířil do jiného adresáře než živý stack a neuměl
> nginx. Dva playbooky, které se rozcházejí, jsou horší než jeden.

### Vars vs. secrets

Nastavení a secrets jsou dva soubory. Necitlivé hodnoty jsou v playbooku
normálním YAMLem; secrets v `member-portal/vault.yml`, zašifrované AES-256.
Ansible vault dešifruje **v paměti na tvém notebooku**, slije obojí do
`env.j2` a výsledek zapíše přes SSH jako `.env` s právy 0600.

Heslo k vaultu se na cílový stroj **nikdy nedostane** — Phoenix o žádném secret
managementu neví, leží tam obyčejný `.env`.

| kontext | co tam je | kdo se k tomu dostane |
|---|---|---|
| Bitwarden (sdílený) | **jen heslo k vaultu** | správci base48 |
| tvůj notebook | plaintext secrets, ale jen v RAM po dobu běhu | ty |
| servers-config-ng (privátní) | zašifrovaný `vault.yml` | kdo má přístup k repu |
| Phoenix | `.env` 0600 root + proměnné v kontejneru | root, skupina `docker` |

Vault obsahuje pět položek: `vault_session_secret`,
`vault_keycloak_client_secret`, `vault_keycloak_service_client_secret`,
`vault_bank_fio_token`, `vault_revbank_api_key`.

### Práce s vaultem

Heslo generuj, nevymýšlej — `ansible-vault` odvozuje klíč přes PBKDF2 s
**10 000 iteracemi**, takže lidmi zvolená fráze je lámatelná offline:

```bash
openssl rand -base64 32        # → do Bitwardenu, sdílet se správci
```

Nový vault zakládej přes `ansible-vault create`, **ne** kopií vzoru a následným
šifrováním — `create` uloží na disk až zašifrovaný výsledek, takže se plaintext
nikdy neocitne v pracovním stromu, kde ho umí sebrat `git add -A`.

```bash
ansible-vault view member-portal/vault.yml     # přečíst
ansible-vault edit member-portal/vault.yml     # upravit
ansible-vault rekey member-portal/vault.yml    # změnit heslo
head -c 30 member-portal/vault.yml             # musí vrátit $ANSIBLE_VAULT;1.1;AES256
```

> **Zašifrovaný vault nepatří do veřejného repa.** Nečitelný je jen dokud
> heslo drží; kdokoliv si ho stáhne a láme offline, jak dlouho chce, a z gitu
> se nikdy nedá vzít zpátky. Commit blokuje pre-commit hook — zapni si ho
> přes `make hooks`.

## Provoz

```bash
docker compose ps                     # stav včetně health
docker compose logs -f portal         # logy serveru
docker compose logs -f cron           # logy sync daemona
docker compose up -d --build          # upgrade po git pull
docker compose restart portal         # restart bez rebuildu
docker compose down                   # zastavit (data zůstávají na hostu)
```

Rotace logů se nastavuje **na hostu**, ne v compose — jednou pro všechny
kontejnery. `/etc/docker/daemon.json`:

```json
{ "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "5" } }
```

Bez toho umí sync cyklus (běží á 2 minuty) zaplnit disk.

### Health

`GET /healthz` vrací `{"status":"ok","database":"ok"}` a 200, nebo 503 když je
databáze nedostupná. Používá to jak `HEALTHCHECK` v image, tak `depends_on`
u cron služby. Endpoint je veřejný a neobsahuje žádná data.

## Zálohování a obnova

V image je `sqlite3`, takže online záloha jde bez odstavení:

```bash
# konzistentní snapshot za běhu
docker compose exec -T portal \
  sqlite3 /app/data/portal.db ".backup /app/data/backup-$(date +%F).db"

# ověření
docker compose exec -T portal \
  sqlite3 /app/data/backup-$(date +%F).db "PRAGMA integrity_check;"

# odklizení z hostu
mv data/backup-$(date +%F).db /var/backups/
```

> **Nekopíruj běžící `portal.db` obyčejným `cp`.** Databáze je v režimu
> `journal_mode=delete`; syrová kopie za běhu může zachytit rozepsanou
> transakci. Vždycky přes `.backup`, nebo se stacku zastaveným.

Playbook zálohu dělá sám: před každým `compose up` odloží kopii do
`/var/backups/member-portal/portal-RRRRMMDD.db`. Server pouští migrace při
startu, takže tohle je jediné okno, ve kterém ještě existují předmigrační data.
Zálohy se nerotují — projdi je občas ručně.

Obnova:

```bash
docker compose down
cp /var/backups/backup-2026-08-27.db data/portal.db
chown 990:985 data/portal.db
docker compose up -d
```

## Reverse proxy

Vhost portálu (na produkci ho spravuje playbook, viz
[Orchestrace a secrets](#orchestrace-a-secrets)):

```nginx
location / {
    proxy_pass http://127.0.0.1:8090;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
}
```

Statiku servíruje **aplikace sama** — proxy ji jen propouští. Vhost tedy
nepotřebuje žádný `alias` ani `root` blok.

Cachování statiky se dá zachovat na proxy:

```nginx
location /static/ {
    proxy_pass http://127.0.0.1:8090;
    proxy_cache_valid 200 7d;
    add_header Cache-Control "public, max-age=604800";
}
```

## Testování proti kopii produkce

`docker-compose.test.yml` je bezpečnostní overlay pro běh nad **kopií ostrých
dat**. Vynuluje service account, SMTP, FIO token i RevBank klíč, takže lokální
běh nemůže sáhnout na produkci.

```bash
# stažení konzistentního snapshotu z produkce
# .timeout: portal i cron drží databázi otevřenou a není ve WAL režimu, takže
# bez čekání na zámek záloha spadne a nechá po sobě nulový soubor
ssh phoenix "sqlite3 -readonly -cmd '.timeout 30000' \
  /var/lib/member-portal/portal.db '.backup /tmp/snap.db' && gzip -f /tmp/snap.db"
scp phoenix:/tmp/snap.db.gz ./
ssh phoenix "rm -f /tmp/snap.db.gz"

mkdir -p data/docker-test
gunzip -c snap.db.gz > data/docker-test/portal.db

PORTAL_UID=$(id -u) PORTAL_GID=$(id -g) \
PORTAL_DATA_DIR=./data/docker-test PORTAL_BIND_PORT=4848 \
docker compose -f docker-compose.yml -f docker-compose.test.yml up -d --build
```

Port **4848** není náhodný — jen `http://localhost:4848/auth/callback` je
zaregistrovaná redirect URI na Keycloak klientovi, takže na jiném portu
nepůjde přihlášení. Viz [KEYCLOAK_SETUP.md](KEYCLOAK_SETUP.md).

Overlay **nikdy nepoužívej na produkčním hostu** — vypnul by tam sync i maily.

## Troubleshooting

**`portal` je `unhealthy`, v logu `unable to open database file`**
Nesedí UID. Zkontroluj `stat -c '%u:%g' $PORTAL_DATA_DIR` proti `PORTAL_UID/GID`.

**V logu cronu `Skipping FIO sync (no BANK_FIO_TOKEN)`**
Prázdný FIO token. Buď je to záměr (test overlay), nebo chybí v `.env`.
Přeskočí se jen stahování plateb; role dluhu i odesílání naplánovaných mailů
běží dál.

**Maily se neodesílají / `smtp dial: connection refused`**
`SMTP_HOST` je `localhost` nebo `127.0.0.1`. Uvnitř kontejneru to je kontejner
sám, ne host. Patří tam `host.docker.internal` (compose ho mapuje na bridge
gateway přes `extra_hosts`). Druhá půlka je na hostu: musí tam běžet MTA na :25,
který přijímá poštu z docker bridge sítě — u postfixu `mynetworks` s
`172.16.0.0/12`. Phoenix má postfix, který poslouchá i na `172.17.0.1:25`.

**Compose hlásí varování o `$` v hodnotě**
Někde v `.env` je dolar. Escapuj ho jako `$$`, nebo hodnotu přegeneruj.

**Změna base image / verze Go**
Base images jsou v `Dockerfile` připnuté digestem. Bump:
`docker pull golang:1.24-alpine`, pak nový digest z `docker images --digests`.

**Chci pustit testy stejně jako CI**
```bash
docker build --target test .
```
Stage `test` není v běžné build cestě — BuildKit ho přeskočí, pokud si o něj
neřekneš.

## Známé provozní dluhy

**Foreign keys se nevynucují.** DSN roky obsahovalo `?_fk=1`, což je syntaxe
ovladače `mattn/go-sqlite3`. Tenhle projekt používá `modernc.org/sqlite`, který
ten parametr přijme a **tiše ignoruje**. Schéma přitom má 15 `REFERENCES` včetně
`ON DELETE CASCADE`.

Zapnout to jde přidáním `_pragma=foreign_keys(1)` do `DATABASE_URL`, ale
**ne naslepo** — nad daty, která roky vznikala bez kontroly, můžou být sirotci
a chování `CASCADE` se změní. Postup:

```bash
# nad KOPIÍ produkční databáze, ne nad ostrou
sqlite3 kopie.db "PRAGMA foreign_key_check;"
```

Když to nic nevypíše, dá se pragma zapnout. Když vypíše řádky, je potřeba je
nejdřív uklidit migrací. Do té doby je stav stejný jako předtím — jen se o něm
aspoň ví.

**`cron` a `depends_on`.** Podmínka `service_healthy` platí jen při prvním
startu stacku. Když se kontejner později restartuje sám (`restart:
unless-stopped`), může naběhnout proti portálu, který je dole. Prakticky to
znamená pár chybových řádků v logu, než se to srovná.

**Zálohy se nerotují.** Playbook přidává jednu kopii na deploy do
`/var/backups/member-portal/` a nikdy nic nemaže.
