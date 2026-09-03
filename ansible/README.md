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
    └── vault.yml                    secrets (šifrované, taky v gitu)
```

## Jak to funguje

Rozdělení je jediná věc, kterou je potřeba pochopit: **nastavení a secrets
jsou dva soubory**. `vars.yml` je normální YAML, který si každý přečte v gitu.
`vault.yml` je stejný YAML, ale zašifrovaný AES-256 heslem — do gitu se commituje
taky, jen jako nečitelný blok. Ansible ho při běhu dešifruje v paměti a obojí
slije do `templates/env.j2`, ze kterého vznikne `.env` na cílovém stroji.

Tím zmizí dnešní stav, kdy secrets žijou ručně editované v
`/etc/nixos/secrets/member-portal.env` mimo jakoukoliv historii a `copyPathToStore`
je navíc kopíruje do world-readable `/nix/store`.

## První nastavení

```bash
pip install --user ansible-core        # nebo dnf install ansible-core
cd ansible
ansible-galaxy collection install -r requirements.yml

cp group_vars/portal/vault.yml.example group_vars/portal/vault.yml
$EDITOR group_vars/portal/vault.yml    # vyplnit reálné hodnoty
ansible-vault encrypt group_vars/portal/vault.yml
```

Heslo k vaultu si zvolíš při šifrování. Sdílí se mimo git — správci si ho
předají osobně nebo přes password manager. Bez něj se s repem nedá deployovat,
což je záměr.

> **Ulož ho na dvě místa dřív, než do vaultu půjdou ostré secrets.** Zašifrovaný
> vault v gitu je k ničemu ve chvíli, kdy k němu nikdo nemá klíč. Dokud heslo
> existuje jen na jednom notebooku, není to záloha secrets — je to jen jiný
> jediný bod selhání než dnešní `/etc/nixos/secrets/`. Sdílený password manager
> plus offline kopie.

Stávající hodnoty se dají zatím vytáhnout ze staré produkce (platí jen do
odstavení Jessicy):

```bash
ssh jessica cat /etc/nixos/secrets/member-portal.env
```

## Deploy

```bash
ansible-playbook deploy.yml --ask-vault-pass
```

Nasadí `portal_version` z `vars.yml` (default `master`). Konkrétní verze:

```bash
ansible-playbook deploy.yml --ask-vault-pass -e portal_version=v1.4.3
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
