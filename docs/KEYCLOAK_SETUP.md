# Keycloak Setup - Dva klienty

Aplikace používá **DVA různé Keycloak klienty** pro různé účely:

## 1. Web Application Client (`go-member-portal-dev`)

**Účel**: Přihlášení běžných uživatelů přes webový prohlížeč

### Nastavení v Keycloaku:

1. **Client ID**: `go-member-portal-dev`
2. **Client Protocol**: `openid-connect`
3. **Access Type**: `confidential` (nebo `public` pokud nepotřebuješ client secret)
4. **Standard Flow Enabled**: `ON` (Authorization Code)
5. **Direct Access Grants Enabled**: `OFF` (není potřeba)
6. **Valid Redirect URIs**:
   - `http://localhost:8080/auth/callback`
   - `https://portal.base48.cz/auth/callback`
7. **Web Origins**: `*` (nebo konkrétní URL)

### Env proměnné:
```bash
KEYCLOAK_CLIENT_ID=go-member-portal-dev
KEYCLOAK_CLIENT_SECRET=PSNf....  # Z Keycloak -> Credentials tab
```

### Použití:
- Uživatelé se přihlašují pomocí `/auth/login`
- Redirect na Keycloak login stránku
- Po přihlášení dostávají role: `memberportal_admin`, `active_member`, `in_debt`
- Token se ukládá do session cookie

---

## 2. Service Account Client (`go-member-portal-service`)

**Účel**: Automatizované úlohy (cron jobs) bez uživatelské interakce

### Nastavení v Keycloaku:

1. **Client ID**: `go-member-portal-service`
2. **Client Protocol**: `openid-connect`
3. **Access Type**: `confidential` ⚠️ **MUSÍ být confidential**
4. **Service Accounts Enabled**: `ON` ⚠️ **DŮLEŽITÉ**
5. **Authorization Enabled**: `ON` (volitelně)
6. **Standard Flow Enabled**: `OFF` (nepotřeba)
7. **Direct Access Grants Enabled**: `OFF` (nepotřeba)

### Service Account Roles:

V záložce **Service Account Roles** přiřaď:

**Client Roles** → `realm-management`:
- `view-users`
- `view-realm`
- `manage-users` (pokud chceš měnit role)

Nebo vytvořit **custom role mappings** pro konkrétní operace.

### Env proměnné:
```bash
KEYCLOAK_SERVICE_ACCOUNT_CLIENT_ID=go-member-portal-service
KEYCLOAK_SERVICE_ACCOUNT_CLIENT_SECRET=R4PXYTcWFl15tQvyQbYbReL3vJ5XC8Ky
```

### Použití:
- Cron joby a automatizace
- Nepotřebuje přihlášeného uživatele
- Získá token pomocí Client Credentials flow
- V audit logu se zobrazí jako "Service account member-portal-service"

---

## Porovnání

| Vlastnost | Web Client | Service Account |
|-----------|-----------|-----------------|
| **Účel** | Přihlášení uživatelů | Automatizace |
| **Flow** | Authorization Code | Client Credentials |
| **Potřebuje uživatele** | Ano | Ne |
| **Access Type** | Confidential/Public | Confidential |
| **Service Accounts** | OFF | **ON** |
| **Audit log** | "User: thebys@..." | "Service account: ..." |
| **Token získání** | Po login redirect | Přímo z API |

---

## Příklady použití

### Web Application (automatické)
```go
// Uživatel se přihlásí přes /auth/login
// Auth middleware automaticky získá token z session
POST /api/admin/roles/assign
```

### Service Account (programatické)
```go
// V cron jobu
serviceClient, _ := auth.NewServiceAccountClient(ctx, cfg,
    "go-member-portal-service",
    "R4PXYTcWFl15tQvyQbYbReL3vJ5XC8Ky")

token, _ := serviceClient.GetAccessToken(ctx)
kcClient := keycloak.NewClient(cfg, token)
kcClient.AssignRoleToUser(ctx, userID, "in_debt")
```

---

## Bezpečnostní poznámky

1. **Client Secret**: Nikdy necommituj do gitu! Používej `.env` soubor
2. **Service Account Permissions**: Přiřaď pouze minimální nutná oprávnění
3. **Token Lifetime**: Service account tokeny mají kratší platnost (obvykle 5-10 minut)
4. **Auto-refresh**: OAuth2 knihovna automaticky refreshuje tokeny
5. **Audit**: Všechny akce service accountu jsou logovány v Keycloaku

---

## Testování

### Test Web Client:
```bash
# Otevři prohlížeč
open http://localhost:8080/auth/login
# Přihlaš se jako admin
# Zkus změnit roli přes /api/admin/roles/assign
```

### Test Service Account:
```bash
# Spusť cron job
go run cmd/cron/update_debt_status.go

# Nebo zkompiluj
go build -o update_debt_status ./cmd/cron/update_debt_status.go
./update_debt_status
```

### Manuální test tokenu:
```bash
# Získej token pro service account
curl -X POST "https://auth.base48.cz/realms/base48/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=go-member-portal-service" \
  -d "client_secret=R4PXYTcWFl15tQvyQbYbReL3vJ5XC8Ky"

# Odpověď obsahuje access_token pro API volání
```

---

## Troubleshooting

### "unauthorized_client"
- Zkontroluj že **Service Accounts Enabled** je `ON`
- Zkontroluj že **Access Type** je `confidential`

### "insufficient_scope" nebo "access_denied"
- Service account nemá přiřazené správné role
- Jdi do **Service Account Roles** a přiřaď `realm-management` → `manage-users`

### "invalid_client"
- Špatný Client ID nebo Secret
- Zkontroluj `.env` soubor

### Token expired
- OAuth2 knihovna automaticky refreshuje, pokud ne:
  - Zkontroluj internet konektivitu
  - Zkontroluj že Keycloak URL je dostupný
