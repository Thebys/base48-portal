# Database Migrations

Tento adresář obsahuje SQL migrace pro Base48 Member Portal.

## Migrace

### 001_initial_schema.sql
Vytvoří základní databázové schema:
- **levels** - úrovně členství (Regular, Student, Support, atd.)
- **users** - členové s profily a Keycloak ID
- **payments** - skutečné platby (FIO bank transactions)
- **fees** - očekávané měsíční poplatky
- Indexy pro rychlé dotazy

**Použití:**
```bash
sqlite3 data/portal.db < migrations/001_initial_schema.sql
```

### 002_import_old_data.sql
Importuje data ze staré `rememberportal.sqlite3` databáze.

**Prerekvizity:**
- Stará databáze v `migrations/rememberportal.sqlite3`
- Vytvořená struktura pomocí `001_initial_schema.sql`

**Co importuje:**
- 12 membership levels
- ~150 users s kompletními profily
- ~3,800 payments od roku 2010
- ~5,000 fees (očekávané měsíční platby)

**Použití:**
```bash
# Vytvoř zálohu
cp data/portal.db data/portal.db.backup

# Spusť import
sqlite3 data/portal.db < migrations/002_import_old_data.sql
```

**Output:**
Skript vypíše shrnutí:
- Počet importovaných uživatelů
- Počet importovaných plateb
- Počet importovaných fees
- Rozsah dat
- Počet orphaned payments (bez uživatele)

## Automatické generování měsíčních poplatků

Po importu dat je potřeba spravovat měsíční poplatky nových období.

### Cron job: create_monthly_fees

**Účel:** Vytváří měsíční fee záznamy pro všechny aktivní členy (`state='accepted'`)

**Použití:**
```bash
# Jednoráz spuštění
go run cmd/cron/create_monthly_fees.go

# Nebo zkompilovaný binary
go build -o create_monthly_fees ./cmd/cron/create_monthly_fees.go
./create_monthly_fees
```

**Crontab nastavení** (1. den v měsíci):
```bash
0 0 1 * * cd /path/to/portal && ./create_monthly_fees >> logs/fees.log 2>&1
```

**Logika:**
- Vytvoří fee pro první den aktuálního měsíce
- Používá `level_actual_amount` (fallback na `level.amount`)
- Kontroluje duplicity - idempotentní (bezpečné opakované spuštění)
- Pouze pro členy se stavem `accepted`

**Příklad output:**
```
Creating fees for period: 2025-12
Processing 57 accepted members...
  ✓ Created fee for user@example.com: 1000 Kč (fee_id: 5028)
  ⊘ Skipping user2@example.com - fee already exists for 2025-12

Summary:
  Period: 2025-12
  Total users: 57
  Created: 56
  Skipped (already exists): 1
  Errors: 0
```

## Mapování staré -> nové databáze

| Stará tabulka | Stará pole | Nová tabulka | Nová pole | Poznámky |
|---------------|------------|--------------|-----------|----------|
| `user` | `id` | `users` | `id` | Zachovává se stejné ID |
| `user` | `email` | `users` | `email` | UNIQUE constraint |
| `user` | `realname` | `users` | `realname` | |
| `user` | `phone` | `users` | `phone` | |
| `user` | `altcontact` | `users` | `alt_contact` | Přejmenováno |
| `user` | `level` | `users` | `level_id` | FK na levels |
| `user` | `payments_id` | `users` | `payments_id` | Variabilní symbol |
| `user` | `state` | `users` | `state` | LOWER() - "Accepted" → "accepted" |
| `user` | `council` | `users` | `is_council` | Přejmenováno |
| `user` | `staff` | `users` | `is_staff` | Přejmenováno |
| `user` | - | `users` | `keycloak_id` | **NULL při importu** - naváže se při prvním loginu |
| `payment` | `user` | `payments` | `user_id` | FK mapování přes temp tabulku |
| `payment` | `json` | `payments` | `raw_data` | FIO bank JSON blob |
| `fee` | `user` | `fees` | `user_id` | FK mapování |
| `fee` | `level` | `fees` | `level_id` | FK na levels |

## Automatické napojení Keycloak ID

Při importu jsou všichni uživatelé bez `keycloak_id` (NULL).

**Proces napojení při prvním přihlášení:**

1. **User se přihlásí přes Keycloak:**
   - Keycloak vrátí ID token s claims (sub, email, roles, ...)

2. **Handler zkusí najít uživatele:**
   ```sql
   SELECT * FROM users WHERE keycloak_id = 'uuid-from-keycloak'
   -- Nenajde (je NULL)
   ```

3. **Fallback na email:**
   ```sql
   SELECT * FROM users WHERE email = 'user@example.com'
   -- Najde importovaného uživatele!
   ```

4. **Automaticky naváže Keycloak ID:**
   ```sql
   UPDATE users SET keycloak_id = 'uuid-from-keycloak'
   WHERE email = 'user@example.com'
   ```

5. **Příští přihlášení:**
   - Najde uživatele rovnou podle `keycloak_id`
   - Rychlejší dotaz (indexed)

## Troubleshooting

### "Error: near line X: UNIQUE constraint failed"
- Email už existuje v databázi
- Zkontroluj duplicity: `SELECT email, COUNT(*) FROM users GROUP BY email HAVING COUNT(*) > 1;`

### "Error: FOREIGN KEY constraint failed"
- Level ID neexistuje v tabulce levels
- Zkontroluj levels: `SELECT * FROM levels;`
- Ujisti se, že `001_initial_schema.sql` proběhla úspěšně

### "Orphaned payments"
- Normální - některé platby nemají přiřazeného uživatele
- Může se stát při platbě před vytvořením účtu
- Adminové je mohou přiřadit později ručně

### Nízký počet importovaných uživatelů
- Skript přeskakuje emaily obsahující `@UNKNOWN` nebo `@unknown`
- Přeskakuje prázdné emaily (`email = '' OR email IS NULL`)
- To je správné chování - placeholder účty se neimportují

## Best Practices

1. **Vždy vytvoř zálohu před importem:**
   ```bash
   cp data/portal.db data/portal.db.backup-$(date +%Y%m%d)
   ```

2. **Testuj na copy databáze:**
   ```bash
   cp data/portal.db data/portal_test.db
   sqlite3 data/portal_test.db < migrations/002_import_old_data.sql
   ```

3. **Kontroluj výsledky:**
   ```bash
   sqlite3 data/portal.db "SELECT COUNT(*) FROM users; SELECT COUNT(*) FROM payments;"
   ```

4. **Po importu zkontroluj integrity:**
   ```bash
   sqlite3 data/portal.db "PRAGMA integrity_check;"
   sqlite3 data/portal.db "PRAGMA foreign_key_check;"
   ```
