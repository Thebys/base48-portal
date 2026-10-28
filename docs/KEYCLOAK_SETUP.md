# Keycloak Setup - Member Portal

Portál používá **dva Keycloak klienty** a tři **realm role**. Produkce:
`https://sso.base48.cz`, realm `hackerspace`. Proměnné prostředí viz `.env.example`.

## Krok 1: Realm roles

**Realm: hackerspace** → **Realm roles** → **Create role**

| Role | Kdo ji přiděluje |
|------|------------------|
| `memberportal_admin` | Ručně, administrátoři portálu |
| `active_member` | Portál: každý člen ve stavu `accepted` |
| `in_debt` | Portál: člen se zápornou bilancí (cron, každé 2 min) |

`active_member` a `in_debt` ručně nepřiřazuj, portál je spravuje sám.

## Krok 2: Web client `member-portal-web`

Přihlášení uživatelů (Authorization Code flow). **Clients** → **Create client**:

```
Client type:               OpenID Connect
Client authentication:     ON
Standard flow:             ON
Direct access grants:      OFF
Service accounts roles:    OFF

Root URL / Home URL:       https://members.base48.cz
Valid redirect URIs:       https://members.base48.cz/auth/callback
                           http://localhost:4848/auth/callback
Valid post logout URIs:    https://members.base48.cz
                           http://localhost:4848
Web origins:               *
```

Secret ze záložky **Credentials** patří do `KEYCLOAK_CLIENT_SECRET`.

## Krok 3: Service account `member-portal-service`

Cron a admin operace s rolemi. **Clients** → **Create client**:

```
Client type:               OpenID Connect
Client authentication:     ON
Standard flow:             OFF
Direct access grants:      OFF
Service accounts roles:    ON   ← důležité
```

**Service account roles** → **Assign role** → **Filter by clients** → `realm-management`:
`view-users`, `manage-users`, `view-realm`, `query-users`.

Secret patří do `KEYCLOAK_SERVICE_ACCOUNT_CLIENT_SECRET`. Kopie proti živému realmu
(lokál, staging) potřebuje `KEYCLOAK_READ_ONLY=true`, jinak cron zapisuje role naostro.

## Krok 4: Role v ID tokenu

Portál čte role z ID tokenu (`realm_access.roles`). Bez tohoto kroku je nevidí.

**Client scopes** → **roles** → **Mappers** → **realm roles** (typ *User Realm Role*,
claim `realm_access.roles`, multivalued) a zapni **Add to ID token**.
Když mapper chybí, vytvoř ho přes **Add mapper** → **By configuration** → **User Realm Role**.

## Ověření

```bash
# Service account: má vrátit JSON s access_token
curl -X POST "https://sso.base48.cz/realms/hackerspace/protocol/openid-connect/token" \
  -d grant_type=client_credentials \
  -d client_id=member-portal-service \
  -d client_secret=<secret>

# Sync rolí (in_debt, active_member)
go run ./cmd/cron sync
```

Web: `make run`, otevři `http://localhost:4848/auth/login`. S rolí
`memberportal_admin` uvidíš v menu Správa.

## Troubleshooting

- **`invalid_client`**: špatné Client ID, secret nebo realm (`KEYCLOAK_REALM`).
- **`unauthorized_client`**: service account nemá zapnuté *Client authentication*
  nebo *Service accounts roles*.
- **`insufficient_scope` / `access_denied`**: service accountu chybí role z `realm-management`.
- **Admin nevidí administraci**: zkontroluj roli `memberportal_admin` a mapper
  z kroku 4, pak se odhlas a přihlas (role se čtou z tokenu při loginu).
