# RevBank integrace

Bar/snack kiosek v Base48 je [RevBank](https://github.com/vega-d/revbank) (fork s
CZK úpravami), Perl CLI na vlastním stroji v lokální síti. Portál na kiosek
nevidí (NAT), takže veškerý sync iniciuje kiosek: skript
[`contrib/revbank-sync.sh`](../contrib/revbank-sync.sh) posílá data na
`POST /api/bar/sync`. Kiosek je zdroj pravdy, portál RevBank nikdy nemění.

## Kiosek

Skript (bash, awk, jq, curl) čte `accounts` a nové `CHECKOUT` řádky z `log`
v `REVBANK_DATA_DIR` (výchozí `/home/kiosk/.revbank`). Pozici v logu drží
v `.sync_cursor`, souběžné běhy blokuje `.sync.lock`. Běží z cronu jako root,
protože data patří uživateli kiosku:

```bash
* * * * * REVBANK_PORTAL_URL=https://members.base48.cz REVBANK_API_KEY=<key> /usr/local/bin/revbank-sync.sh
```

Lokálně: `REVBANK_DATA_DIR=... REVBANK_PORTAL_URL=http://localhost:4848 REVBANK_API_KEY=test bash contrib/revbank-sync.sh`.

## API: `POST /api/bar/sync`

`Authorization: Bearer <REVBANK_API_KEY>`, porovnání v konstantním čase.
Staré `/api/revbank/sync` přesměrovává sem (308).

```json
{
  "accounts": [{"username": "thebys", "balance_cents": 2500, "last_transaction_at": "2026-03-10_22:36:06"}],
  "transactions": [{"id": "2026-03-10_22:36:06_thebys_0", "username": "thebys", "amount_cents": -2000,
                    "description": "bramborky", "counter_account": "+sales/products", "created_at": "2026-03-10_22:36:06"}],
  "system_accounts": {"-cash": -12000}
}
```

Odpověď: `{"accounts_synced", "transactions_synced", "users_matched", "users_unmatched"}`.

- Účty a transakce se upsertují (`revbank_accounts`, `revbank_transactions`),
  systémové účty (`+`, `-`, `*`) se mezi uživatele nepočítají.
- Systémové zůstatky a čas posledního syncu jdou do `settings`
  (`revbank_system_accounts`, `revbank_last_sync`).
- Člen se páruje podle `users.username` bez ohledu na velikost písmen, a to
  při každém syncu znovu. Přejmenování v Keycloaku tak bar účet tiše odpojí.
- Portál jen upsertuje: co zmizí na kiosku, zůstane v portálu jako starý řádek.

## Portál

- **Profil**: zůstatek a poslední nákupy (box *Bar / Kiosek*).
- **Správa → Bar** (`/admin/bar`): účty, transakce, analytika, generátor
  kartiček s čárovými kódy a tisknutelné návody (nákup, dobíjení).
- **Správa → Nastavení**: upomínky dluhu na baru (práh, interval, zpoždění),
  posílá je `portal-cron bar-debt-emails`.

Neimplementováno: správa produktů z portálu, VS pro dobíjení baru, storno.

## Známý dluh: `created_at` se ukládá jako wall clock

RevBank loguje svůj vlastní čas jako `2026-03-07_04:09:09` — **lokální čas
kiosku**, bez zóny. Sync ho posílá beze změny a `parseFlexibleTime`
(`internal/handler/bar.go`) ho čte bezzónovým layoutem, takže Go ten wall clock
označí za UTC. V databázi pak stojí `2026-03-07 04:09:09 +0000 UTC`, i když ten
okamžik ve skutečnosti nastal v 04:09 pražského času, tedy 03:09 UTC.

Dva důsledky:

1. **Uložený okamžik je o hodinu až dvě posunutý.** Kdo si ta data načte a
   převede do Evropy/Praha (což vypadá jako správný krok), posune je podruhé —
   páteční večer se mu rozlije do soboty.
2. **`date()` a `strftime()` nad tím vrací NULL**, protože sufix ` +0000 UTC`
   SQLite neumí. Proto `computeBarAnalytics` agreguje v Go a ne v SQL.

Analytika s tím pracuje vědomě: bere uložené hodnoty jako wall clock a do stejného
prostoru si přeloží jen `time.Now()` (`barWallNow`). Šablony ze stejného důvodu
timestampy **neformátují přes žádnou konverzi** — syrová hodnota je ta správná
k zobrazení.

**Správná oprava je na vstupu**, ne na výstupu: parsovat v `Europe/Prague`
a ukládat ISO-8601 v UTC. Tím se okamžiky narovnají, `strftime()` začne fungovat
a celý wall-clock trik zmizí. Chce to ale migraci, která přepíše existující
řádky (posun o platný offset k datu každého řádku, tedy CET vs. CEST), takže je
to samostatná změna s vlastní zálohou — ne něco, co se přibalí k featuře.
