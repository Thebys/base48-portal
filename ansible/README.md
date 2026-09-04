# Ansible deployment

Nasazení portálu na hosta, kde už běží Docker. Ansible dělá tři věci:
naklonuje repo v dané verzi, vygeneruje `.env` ze zašifrovaného vaultu
a pustí `docker compose up`.

```
ansible/
├── deploy.yml                       playbook — celý postup
├── inventory.yml                    které stroje
├── requirements.yml                 kolekce community.docker
├── templates/env.j2                 šablona .env souboru
└── group_vars/portal/
    ├── vars.yml                     nastavení (necitlivé, čitelné v gitu)
    └── vault.yml.example            vzor — reálný vault sem NEPATŘÍ, viz níž
```

> ## ⚠ Vault nepatří do tohohle repa
>
> **`Thebys/base48-portal` je veřejné repo.** Zašifrovaný vault je sice
> nečitelný, ale kdokoliv si ho stáhne a láme offline, jak dlouho chce —
> a z gitu se nikdy nedá vzít zpátky. `ansible-vault` navíc odvozuje klíč
> přes PBKDF2 s **10 000 iteracemi**; OWASP dnes doporučuje 600 000+.
>
> Reálný `vault.yml` proto žije v **`base48/servers-config-ng`**, které je
> privátní. Tady je jen `vault.yml.example` jako vzor struktury.
>
> Commit vaultu do tohohle repa blokuje pre-commit hook. Zapni si ho:
> `make hooks`.

## Jak to funguje

Rozdělení je jediná věc, kterou je potřeba pochopit: **nastavení a secrets
jsou dva soubory**. `vars.yml` je normální YAML, který si každý přečte v gitu.
`vault.yml` je stejný YAML, ale zašifrovaný AES-256 heslem. Ansible ho při běhu
dešifruje **v paměti na tvém notebooku**, slije obojí do `templates/env.j2`
a výsledek zapíše přes SSH jako `.env` na cílový stroj.

Heslo k vaultu se na cílový stroj **nikdy nedostane**. Phoenix nemá o žádném
secret managementu ponětí — leží tam obyčejný `.env` s právy 0600.

Tím zmizí dnešní stav, kdy secrets žijou ručně editované v
`/etc/nixos/secrets/member-portal.env` mimo jakoukoliv historii a `copyPathToStore`
je navíc kopíruje do world-readable `/nix/store`.

## Kde se co děje

| kontext | co tam je | kdo se k tomu dostane |
|---|---|---|
| Bitwarden (sdílený) | **jen heslo k vaultu** | správci base48 |
| tvůj notebook | plaintext secrets, ale jen v RAM po dobu běhu | ty |
| git (servers-config-ng, privátní) | zašifrovaný `vault.yml` | kdo má přístup k repu |
| Phoenix | `.env` 0600 root + proměnné v kontejneru | root, skupina `docker` |
| Jessica | dnešní zdroj pravdy, plaintext | root — dokud ji neodstavíme |

## První nastavení

Heslo generuj, nevymýšlej — při 10k iteracích PBKDF2 je lidmi zvolená fráze
lámatelná:

```bash
openssl rand -base64 32        # → do Bitwardenu, sdílet se správci
```

Vault zakládej přes `ansible-vault create`, **ne** kopií vzoru a následným
šifrováním. `create` otevře editor nad dočasným souborem a na disk uloží až
zašifrovaný výsledek — plaintext secrets se tak nikdy neocitnou v pracovním
stromu gitu, kde je umí sebrat `git add -A`:

```bash
cd <servers-config-ng>/portal
ansible-vault create vault.yml      # NE: cp vault.yml.example vault.yml
```

Strukturu opiš z [vault.yml.example](group_vars/portal/vault.yml.example).
Po uložení ověř, že je to opravdu zašifrované:

```bash
head -c 30 vault.yml                # musí vrátit $ANSIBLE_VAULT;1.1;AES256
```

Stávající hodnoty se dají zatím vytáhnout ze staré produkce (platí jen do
odstavení Jessicy). Pozor na shell historii — proto `ssh` rovnou do editoru,
ne přes proměnné:

```bash
ssh jessica cat /etc/nixos/secrets/member-portal.env
```

## Deploy

```bash
ansible-playbook deploy.yml --ask-vault-pass
```

Nasadí `portal_version` z `vars.yml` (default `master`). Konkrétní verze:

```bash
ansible-playbook deploy.yml --ask-vault-pass -e portal_version=v1.6.1
```

Suchý běh — ukáže, co by se změnilo, bez zásahu:

```bash
ansible-playbook deploy.yml --ask-vault-pass --check --diff
```

Dvě omezení, která stojí za to znát dopředu:

- Na hostu, kde ještě nikdy neproběhl ostrý deploy, `--check` doběhne jen
  částečně. Adresáře ani git checkout v check módu nevzniknou, takže tasky,
  které na ně navazují, se přeskočí nebo spadnou. Nanečisto se ověřuje
  **opakovaný** deploy, ne ten první.
- Task, který píše `.env`, má `no_log: true`, protože jsou v něm secrets. To
  potlačí i `--diff`, takže u jediného souboru, který se reálně mění, neuvidíš
  obsah. Je to záměr — ne rozbitý diff.

Playbook je idempotentní. Když se nic nezměnilo, doběhne bez `changed`.
Když se změní `.env`, kontejnery se vynuceně recreatnou — compose sám změnu
obsahu env souboru spolehlivě nepozná.

## Práce s vaultem

```bash
ansible-vault view group_vars/portal/vault.yml     # přečíst
ansible-vault edit group_vars/portal/vault.yml     # upravit (dešifruje do editoru)
ansible-vault rekey group_vars/portal/vault.yml    # změnit heslo vaultu
```

Aby se heslo nemuselo psát pořád dokola, jde uložit do souboru mimo repo
a odkomentovat `vault_password_file` v `ansible.cfg`.

## Na co si dát pozor

**Nepouštěj `docker compose config` na produkci do sdíleného výstupu.**
Vypíše celé prostředí včetně secrets v plain textu. Platí i pro logy z CI
a přílohy k ticketům.

**Vault neřeší expozici na hostu, jen v gitu a v historii.** Do kontejneru jdou
secrets jako proměnné prostředí, takže je přečte `docker inspect` — a tím
kdokoliv ve skupině `docker`. Proti dnešnímu stavu je to zlepšení (secrets
přestanou být ručně editované mimo historii a world-readable v `/nix/store`),
ale root na hostu je pořád root.

**`vault.yml` po `ansible-vault encrypt` zkontroluj.** `head -c 30` musí vrátit
`$ANSIBLE_VAULT;1.1;AES256`. Když tam je čitelný YAML, není zašifrovaný.

**Playbook nenasazuje Docker.** Předpokládá, že na hostu už je Docker Engine
s Compose v2. První task to ověří a spadne, když ne.

**Log rotace patří na hosta, ne do compose.** V `/etc/docker/daemon.json`:

```json
{ "log-driver": "json-file", "log-opts": { "max-size": "10m", "max-file": "5" } }
```

Nastavené jednou pro všechny kontejnery na stroji.

## Až přibudou další služby

Teď je to jeden plochý playbook, protože je to jedna aplikace. Až na hosta
přijde mediawiki nebo mailman, dává smysl to překlopit do rolí
(`roles/portal/`, `roles/mediawiki/`) a `deploy.yml` nechat jen jako seznam,
co kam patří. Do té doby by role byla ceremonie navíc.
