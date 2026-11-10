# Authentication

The same design as journal-server-go's [authentication.md](https://github.com/chencohensolutions/journal-server-go/blob/main/docs/authentication.md),
and the same code (`enroll/`, `auth/access.go`, `middleware.go`). The differences:

- The news server has its own CA (`news-server-ca`), so a journal certificate does not open it, and the reverse.
- The QR code holds `news://enroll?code=…&server=…&fingerprint=…&key=…&address=…`, for the news app. `fingerprint` is the SHA-256
  of the server certificate's public key (`tls_cert`), in hex. The app pins that key from its first connection
  instead of trusting a CA and the host name, so the self-made CA and an IP address both work and nothing is built
  into the app. `enroll-code` also prints its first 16 digits as `Key`; when the code is typed instead of scanned,
  the app shows the key it sees for you to compare.
- No peers and no private files. The app finds the server by the link's addresses first: `public_url`, else the
  router's outside address once the port is mapped, then the server's own addresses (up to six `address=`).
- **Away from home:** the server asks the home router to forward its port (UPnP or NAT-PMP, journal-server-go's
  `portmap`), renews it, and removes it on Ctrl+C or a service stop. `"port_mapping": false` turns it off. Behind
  carrier-grade NAT the log says the mapping can't help.
- **When the home IP changes:** the server publishes its addresses in the BitTorrent DHT (journal-server-go's
  `discovery`, BEP 44 with salt `news-server/v1`), signed with a key derived from its TLS key and sealed so only
  someone holding the link's `key=` can read them. The app looks there when no known address answers, and
  `serverInfo.discovery` gives it the current list while connected. `"discovery": false` turns it off;
  `news-server discovery` publishes and reads it back, to check it works.
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
