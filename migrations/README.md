# Databázové migrace

Migrace jsou zabalené v binárce (`embed.go`) a server je spouští sám při startu
(`internal/migrate`). Aplikované verze si pamatuje v tabulce `schema_migrations`;
na starší DB bez ní při prvním běhu dopočítá, co už v ní je.

- Nová migrace dostane další číslo: `NNN_popis.sql`. Spouští se jako jeden skript
  bez transakce, takže ji piš idempotentně (`IF NOT EXISTS`, guard na `UPDATE`).
- Už aplikovanou migraci neměň, napiš novou.
- Mění-li schéma, přidej ji do `schema` v `sqlc.yaml` a spusť `make sqlc`.

`002_import_old_data.sql` se přeskakuje. Je to jednorázový import ze staré
`rememberportal.sqlite3` a spouštěl se ručně:

```bash
cp data/portal.db data/portal.db.backup
sqlite3 data/portal.db < migrations/002_import_old_data.sql
```

Importovaní členové nemají `keycloak_id`, napojí se při prvním přihlášení podle e-mailu.
