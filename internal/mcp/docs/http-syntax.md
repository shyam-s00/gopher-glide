# gg `.http` file syntax

gg reads the standard `.http` request format (JetBrains HTTP Client / VS Code REST Client style)
and adds one extension, `@gg-export`, for passing values between requests.

## Requests

Requests are separated by a line starting with `###`; text after `###` is an optional name.

```http
### List products
GET https://api.example.com/products?limit=10
Accept: application/json

### Create order
POST https://api.example.com/orders
Content-Type: application/json
Accept: application/json

{
  "sku": "A-100",
  "qty": 1
}
```

- Request line: `METHOD URL`. A bare `URL` means `GET`. Methods: GET, POST, PUT, DELETE, PATCH,
  HEAD, OPTIONS, CONNECT, TRACE.
- Headers follow the request line, one `Name: value` per line. A blank line ends the headers.
- Everything after the blank line is the body (up to the next `###`).
- Lines starting with `#` or `//` are comments before the body. Inside a body they are content.
- Requests without a journey link (below) are independent and are dispatched round-robin.

## Built-in dynamic values

Usable in the URL, header values and body. Each occurrence gets a fresh value.

| Placeholder | Value |
|---|---|
| `{{$uuid}}` | random UUID v4 |
| `{{$randomInt}}` | random integer from 0 to 1,000,000 |
| `{{$timestamp}}` | current Unix time in milliseconds |

```http
### Sign up
POST https://api.example.com/users
Content-Type: application/json
X-Request-ID: {{$uuid}}

{"email": "load-{{$randomInt}}@example.com", "createdAt": {{$timestamp}}}
```

## Chaining requests: `@gg-export`

Put `# @gg-export <name> = <engine>: <pattern>` after a request's headers (or after its body).
After the response arrives, the pattern runs against the response body and the result is stored
as `<name>`. Later requests use it as `{{name}}`.

```http
### Login
POST https://api.example.com/login
Content-Type: application/json

{"user": "demo", "password": "demo"}

# @gg-export token = jsonpath: $.data.token

### Profile
GET https://api.example.com/me
Authorization: Bearer {{token}}
```

Engines:

- `jsonpath` — dot notation with optional leading `$`, and `[n]` array indexes:
  `$.data.token`, `$.items[0].id`, `data.users[2].name`. No wildcards or filters.
- `regex` — Go (RE2) syntax. The first capture group of the first match is used; with no
  capture group, the whole match.

Several `@gg-export` lines may follow one request. Values are strings.

## Journeys

gg groups requests automatically. A request that has an `@gg-export`, and every following request
that uses one of the exported names as `{{name}}`, form one **journey**. Each virtual user runs a
journey from top to bottom with its own variables. Every other request is a one-step journey.

- A journey stops at its first failed step (transport error, or an `@gg-export` that finds
  nothing). Remaining steps do not run for that pass.
- A request that uses `{{name}}` before any request exports `name` is not part of a journey and
  would send the literal text `{{name}}`. Put the exporting request first.
- Only `{{name}}` with word characters (`a-z A-Z 0-9 _`) is a variable. `{{$...}}` is a built-in.

## Tips for writing load-test files

- Use one `###` block per distinct endpoint you want to exercise; the mix is round-robin.
- Keep bodies realistic but small; gg measures latency, not payload handling.
- Prefer built-in dynamic values for unique fields (ids, emails) to avoid cache-only traffic.
- Keep secrets out of the file; pass them through the environment of your own tooling.
