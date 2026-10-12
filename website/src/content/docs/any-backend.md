---
title: "Works With Any Backend"
description: "gg is an HTTP traffic and regression simulator for any API — Node.js, Python, Java, .NET, Rails, PHP, Rust, Go or anything else that speaks HTTP. Nothing to install in your app."
---

`gg` doesn't care what your service is written in. It's an outside-in HTTP client: it sends requests to a URL, measures what comes back, and (with [snapshots](/snap)) tells you when behaviour or latency drifts between builds. Express, FastAPI, Spring Boot, Rails, ASP.NET Core, Laravel, Axum, Phoenix, a Go service, a legacy monolith, a serverless function behind a gateway — to `gg` they are all just URLs.

> **What `gg` needs from your service:** a URL that `gg` can reach. That's the whole list. No agent, no SDK, no library in your dependency tree, no code change and no restart.

---

## Try it against your stack

Three steps, and only the first one depends on your language.

### 1. Start your service the way you normally do

| Stack | Typical dev command | Common default URL |
|---|---|---|
| Node.js (Express, Fastify, Nest) | `node server.js` / `npm run dev` | `http://localhost:3000` |
| Python (FastAPI) | `uvicorn main:app` | `http://localhost:8000` |
| Python (Flask) | `flask run` | `http://localhost:5000` |
| Python (Django) | `python manage.py runserver` | `http://localhost:8000` |
| Java / Kotlin (Spring Boot) | `./mvnw spring-boot:run` / `./gradlew bootRun` | `http://localhost:8080` |
| .NET (ASP.NET Core) | `dotnet run` | set in `launchSettings.json` |
| Ruby on Rails | `bin/rails server` | `http://localhost:3000` |
| PHP (Laravel) | `php artisan serve` | `http://localhost:8000` |
| Rust (Axum, Actix) | `cargo run` | set in your code |
| Elixir (Phoenix) | `mix phx.server` | `http://localhost:4000` |
| Go (`net/http`, Gin, Echo) | `go run .` | set in your code |

Those ports are the frameworks' usual defaults; yours may differ. Use whatever URL you'd open in a browser or `curl`.

### 2. Describe the traffic in a `.http` file

```http
GET http://localhost:3000/health
Accept: application/json

###

POST http://localhost:3000/orders
Content-Type: application/json

{ "sku": "ABC-123", "qty": 2 }
```

This is the same request format your editor already understands. If your repo has `.http` files for manual testing, you can point `gg` straight at them.

### 3. Run it

```bash
gg --profile load --peak-rps 50 --duration 30s --http-file api.http
```

You get the live TUI by default. Add `--headless --reporter json` for scripts and CI. See [Load Profiles](/profiles) for the built-in traffic shapes.

---

## Reaching your service from `gg`

| `gg` runs… | Your service runs… | Use this host in the `.http` file |
|---|---|---|
| on your machine | on your machine | `localhost` |
| in Docker | on your machine | `host.docker.internal` (Docker Desktop) |
| in Docker, same Compose network | in another container | the Compose service name, e.g. `http://api:8080` |
| in CI | as a service container or Compose stack | the service name or `localhost`, depending on how the job is wired — see [CI & Containers](/ci) |
| anywhere | deployed (staging, a preview environment) | its public or internal URL |

Only load-test systems you own or have explicit permission to test.

---

## In the editor you already use

The [JetBrains and VS Code plugins](/plugin) run `.http` files with gutter icons, a live dashboard and a snapshot diff explorer. They work in IntelliJ IDEA, PyCharm, WebStorm, Rider, PhpStorm, RubyMine, GoLand and VS Code, so you don't have to switch tools to use them with a non-Go project.

## Regression gates for any pipeline

`gg snap` records a run and `gg snap assert` compares it to a baseline, failing the build when latency, error rate or response shape regresses. It works the same whether the service under test is Python or Java, and from any CI system that can run a binary or a container. Start with [CI & Containers](/ci) and [Snapshots](/snap).

---

## So why "Gopher"?

`gg` is written in Go, and the name is a nod to that. It's an implementation detail rather than a requirement:

- You **don't need Go installed**, and you don't need to know it.
- Being a compiled Go program is why `gg` ships as a **single static binary** with no JVM, Node or Python runtime to install next to your app, and why one instance can generate 30,000+ requests per second.
- Nothing about the service you test has to be Go.

Ready? Head to [Getting Started](/getting-started).
