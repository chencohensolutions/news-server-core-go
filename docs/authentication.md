# Authentication

The same design as journal-server-go's [authentication.md](https://github.com/chencohensolutions/journal-server-go/blob/main/docs/authentication.md),
and the same code (`enroll/`, `auth/access.go`, `middleware.go`). The differences:

- The news server has its own CA (`news-server-ca`), so a journal certificate does not open it, and the reverse.
- The QR code holds `news://enroll?code=…&server=…`, for the news app.
- No peers, no private files, no discovery through the DHT: `public_url` is the address the app uses.
- Port 56399 and admin API 127.0.0.1:8082 by default, beside the journal server's 56398 and 8081.

## Access rules

| Endpoint | Without client certificate | With a valid client certificate |
|---|---|---|
| `POST /query` (GraphQL) | Only `enrollmentPreview` and `enrollDevice`. Everything else, introspection too, returns `client certificate required for <field>` with `extensions.code` `UNAUTHORIZED`. | Every field |
| `GET /` (playground) | Allowed (it can only call `@public` fields) | Allowed |
| Admin API (`admin_addr`) | Loopback only, needs the admin token | n/a |

A certificate's CN must be a `username` in `config.json`, and its serial must not be revoked in `devices.json`;
otherwise `401`. Enrollment codes are 8 characters, valid 10 minutes, single use, kept in memory only (a restart
drops them), and limited to 5 wrong tries per IP per minute.
