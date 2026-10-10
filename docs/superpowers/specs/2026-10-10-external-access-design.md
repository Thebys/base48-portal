# Externí uživatelé a oprávnění — návrh

**Datum:** 2026-10-10
**Stav:** Schváleno 2026-10-10

## Proč

Portál zná jen členy a uchazeče. Kdo není ani jedno, a přesto potřebuje
část portálu (zatím kalendář autodílny), skončí po přihlášení jako uchazeč:
vidí výzvu čekat na schválení, rada ho má ve frontě a autodílna je pro něj
zavřená.

## Návrh

Dvě nezávislé osy:

- **Stav** (`users.state`) říká, kým je člověk pro spolek. Nový stav
  `external`: není člen ani uchazeč.
- **Oprávnění** (`access_grants`) dávají přístup k části portálu nezávisle
  na stavu. První rozsah je `workshop`. Členové mají přístup ze stavu,
  oprávnění přístup jen přidává.

Poplatky, role v Keycloaku, upomínky, bar karty a export členů se týkají
členství a zůstávají na `accepted`.

Pravidla jsou v `internal/access`, používají je handlery i šablony
(`.Access`):

| stav | Profil | Fundraising | Finance, Služby, Poslední platby | Autodílna | Úroveň a příspěvek |
|---|---|---|---|---|---|
| `accepted` | ✓ | ✓ | ✓ | ✓ | ✓ |
| `awaiting` | banner uchazeče | – | – | G | – |
| `suspended`, `exmember`, `rejected` | platby a bilance | – | – | G | – |
| `external` | banner externího | ✓ | – | G | – |
| admin | ✓ | ✓ | ✓ | ✓ | podle vlastního stavu |

G = jen s oprávněním `workshop`.

Nečlenové nevidí e-maily členů v kalendáři autodílny (kdo nemá přezdívku,
je „(bez přezdívky)“) ani čísla účtů dárců ve fundraisingu.

## Administrace

- Stav `external` se nastavuje na profilu uživatele, seznam uživatelů má
  předvolbu „Externí“.
- Sekce Oprávnění na profilu uživatele: přidat s poznámkou, odebrat. Obojí
  se loguje (subsystem `access`).
- Po odebrání oprávnění zůstávají budoucí rezervace, zrušit je může admin.

## Migrace

- `018` přestaví `users`, protože SQLite neumí změnit CHECK. Soubor si
  řídí transakci sám a `foreign_keys` vypíná jen na dobu přestavby.
- `019` přidá `access_grants`.
- Rollback na 1.10.0 přestavěnou tabulku čte a `access_grants` ignoruje.
  Externí uživatel by ale viděl členské menu (rezervovat nemůže).

## Známá omezení

- Bývalý člen přepnutý na `external` s dluhem svou bilanci na profilu
  neuvidí a upomínky mu nechodí. V adminu je dluh vidět dál.
