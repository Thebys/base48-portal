# Deployment: Docker / Compose

Nasazení member portálu přes `docker compose`. **Tohle je jediný podporovaný
způsob nasazení** — Nix balení (`default.nix`) bylo z repa odstraněno.

Tenhle dokument popisuje **stack samotný**. Orchestraci a secrets řeší Ansible —
viz [ansible/README.md](../ansible/README.md). Ruční postup níž je použitelný
pro dev a jako fallback, když je potřeba sáhnout na hosta přímo.

> Migrace z Jessicy zatím **neproběhla**. Kapitola [Cutover](#cutover-z-jessicy-nixos-na-phoenix-docker)
> a odkazy na NixOS v ní jsou dočasné — až bude Jessica odstavená, jde celá
> pryč jedním commitem.

## Architektura

Stack je **app-only** — neterminuje TLS a neřeší vhosty. To zůstává na reverse
proxy, kterou už host má.

```
internet :443
    │
    ├─ haproxy        SNI router (jessica)
    │      │
    │      └─ nginx 127.0.0.1:1443    TLS termination, ACME, vhost
    │              │
    │              └─ proxy_pass ──► 127.0.0.1:8090
    │                                     │
    └─────────────────────────────────────┴─► kontejner portal :8080
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
| `cron`   | `/app/portal-cron daemon` | FIO sync á 2 min, poplatky 1. v měsíci, maily      |

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
v `group_vars`). Playbook uživatele zakládá s těmito čísly, nenechává si je
přidělit. Důvod je stěhování mezi stroji: číselné vlastnictví souboru musí
znamenat totéž na Jessice i na Phoenixu, jinak zkopírovaná `portal.db` skončí
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

Nginx vhost, který nahrazuje ten z `base48-portal.nix`:

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

> Past při migraci z Jessicy: tamní nginx má `location /static/` s `alias` do
> `/nix/store/…`. Do nového vhostu ho **nekopíruj** — po cutoveru by portál
> vracel 404 na CSS a JS.

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
ssh jessica "nix-shell -p sqlite --run \
  'sqlite3 -readonly /var/lib/member-portal/portal.db \".backup /tmp/snap.db\"' \
  && gzip -f /tmp/snap.db"
scp jessica:/tmp/snap.db.gz ./
ssh jessica "rm -f /tmp/snap.db.gz"

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

## Cutover z Jessicy (NixOS) na Phoenix (Docker)

> **Dočasná kapitola.** Platí jen dokud běží stará produkce na Jessice. Po
> odstavení a ověření ji smaž i s odkazy na NixOS jinde v repu.

Není to výměna na místě — data se stěhují na jiný stroj, takže přibývá kopie
databáze, nový vhost a přepnutí DNS. Jessica zůstává nedotčená až do konce,
což je zároveň rollback.

**0. Předpoklady na Phoenixu**

Docker Engine a Compose v2 tam už jsou. Co chybí, je rotace logů — nastav ji
dřív, než se cokoliv rozjede, jinak sync smyčka á 2 minuty zaplní disk:

```bash
# /etc/docker/daemon.json
{ "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "5" } }
```

**1. Příprava (bez výpadku)**

Naplň vault podle [ansible/README.md](../ansible/README.md) a nech playbook
založit uživatele, adresáře, `.env` a stack. Portál poběží na Phoenixu naprázdno
s prázdnou databází — DNS pořád míří na Jessicu, takže se nikoho nedotkne.

```bash
cd ansible
ansible-playbook deploy.yml --ask-vault-pass
curl -s localhost:8090/healthz     # na phoenixu
```

**2. Nginx vhost na Phoenixu**

Nový `members.base48.cz` podle vzoru ostatních vhostů, plus certifikát:

```bash
certbot --nginx -d members.base48.cz
```

Blok `location /static/` s `alias` do nix store **nekopíruj** — v Dockeru
statiku servíruje aplikace sama.

**3. Cutover (výpadek ~2 min)**

```bash
# na jessice: zastavit a udělat finální konzistentní snapshot
systemctl stop member-portal-cron member-portal
nix-shell -p sqlite --run \
  "sqlite3 -readonly /var/lib/member-portal/portal.db \
   '.backup /tmp/portal-cutover.db'"

# přenos se zachováním práv
scp -p /tmp/portal-cutover.db phoenix:/tmp/

# na phoenixu
docker compose -f /opt/base48-portal/docker-compose.yml down
install -o 990 -g 985 -m 0600 /tmp/portal-cutover.db \
  /var/lib/member-portal/portal.db
cd /opt/base48-portal && docker compose up -d
curl -s localhost:8090/healthz
```

UID `990:985` sedí na obou strojích, protože je playbook připíná — ale po
`install` si to stejně ověř přes `stat`.

**4. DNS** — `members.base48.cz` dnes míří přes `jessica.base48.cz` na
`37.205.13.28`. Přepni na Phoenix (`194.182.84.91`). Než se to rozšíří, běží
obojí; Jessica je zastavená, takže nehrozí, že by dva cronů zapisovaly do dvou
různých databází.

**5. Ověření** — homepage, přihlášení přes Keycloak, `/admin/users`, `/static/`
assety, a v logu cronu úspěšný FIO sync do dvou minut.

**6. Úklid až po ověření (klidně za týden)**

```bash
# na jessice
systemctl disable member-portal member-portal-cron
# services.base48-portal.enable = false; v configuration.nix
```

### Rollback

Dokud je na Jessice nix modul jen zastavený a DNS se dá vrátit, je návrat
otázkou minut:

```bash
# na phoenixu
docker compose down
# na jessice
systemctl start member-portal member-portal-cron
# a vrátit DNS zpět na 37.205.13.28
```

Databáze na Jessice zůstala nedotčená v původním stavu, takže se nic nemigruje
zpátky. Ztratí se jen zápisy, které mezitím proběhly na Phoenixu — proto se
ověřuje hned a rollback se rozhoduje rychle.

## Troubleshooting

**`portal` je `unhealthy`, v logu `unable to open database file`**
Nesedí UID. Zkontroluj `stat -c '%u:%g' $PORTAL_DATA_DIR` proti `PORTAL_UID/GID`.

**`cron` se restartuje dokola / `BANK_FIO_TOKEN is required`**
Prázdný FIO token. Buď je to záměr (test overlay), nebo chybí v `.env`.
Pozor: `runSync` se při chybějícím tokenu ukončí hned, takže se přeskočí
i aktualizace dluhů a odesílání mailů.

**Maily se neodesílají / `smtp dial: connection refused`**
`SMTP_HOST` je `localhost` nebo `127.0.0.1`. Uvnitř kontejneru to je kontejner
sám, ne host. Patří tam `host.docker.internal` (compose ho mapuje na bridge
gateway přes `extra_hosts`). Druhá půlka je na hostu: musí tam běžet MTA na :25,
který přijímá poštu z docker bridge sítě — u postfixu `mynetworks` s
`172.16.0.0/12`. **Phoenix zatím žádný MTA nemá**, Jessica má postfix.

**Statika vrací 404 po cutoveru**
V nginxu zůstal `alias` na nix store. Viz sekce Reverse proxy.

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
