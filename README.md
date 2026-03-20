# HO (humans. Out.)

---

## Not your average HO

**One binary. Describe what you want. Done.**

HO autonomously builds, deploys, and monitors web(sites/apps/games).

No Docker. No npm. No database server. No webserver. No build steps. Just run it.

Also, your data stays on your server/computer. Nothing gets hosted in the cloud.


---

## Quick Start

Download from **[Releases](https://github.com/markdr-hue/HO/releases)**, run the binary, open `http://localhost:5001`.
The setup wizard handles the rest in ~2 minutes. Your site is served at `http://localhost:5000`.

```bash
# Linux / macOS
chmod +x ./ho && ./ho

# Windows
.\ho.exe
```

## The Pipeline

Every project goes through the same deterministic build process:

| Stage | What Happens |
|---|---|
| **PLAN** | Analyzes your description, asks clarifying questions if needed, produces a structured Plan (pages, endpoints, tables, design system) |
| **BUILD** | Single LLM session: creates all tables, endpoints, CSS, layout, JS, and pages. Dynamic iteration budget scales with plan complexity |
| **VALIDATE** | Verifies every plan item was actually created. Auto-fixes missing items. Runs design consistency review |
| **COMPLETE** | Switches to monitoring mode |
| **MONITORING** | Adaptive health checks (5-15 min), investigates and self-heals issues |

Updates use **UPDATE_PLAN** — patches only affected parts of the plan, then re-enters BUILD.

---

## Features

### AI & Autonomy
- **Autonomous building** — describe what you want (site, webapp, SPA, multiplayer game, dashboard). HO plans, designs, codes, and deploys it
- **Human-in-the-loop** — asks clarifying questions when your description is vague, pauses until you answer
- **Incremental updates** — tell HO what to change via chat, only affected parts get rebuilt
- **Self-healing monitoring** — adaptive health checks, automatic issue investigation
- **Crash recovery** — pipeline resumes exactly where it left off
- **Persistent memory** — remembers preferences, decisions, and context across chat sessions
- **Post-build validation** — structural checks, functional HTTP tests, design consistency review

### Data & APIs
- **Dynamic databases** — SQLite tables created on the fly with secure column types (PASSWORD via bcrypt, ENCRYPTED via AES-256-GCM)
- **Auto-generated REST APIs** — CRUD endpoints with filtering (`?col=val`, `?col__like=`, `?col__gt=`), sorting, pagination, field selection, aggregation (COUNT, SUM, AVG, MIN, MAX + GROUP BY)
- **Full-text search** — FTS5 indexes on any text column with phrase, prefix, and boolean queries
- **OpenAPI docs** — auto-generated Swagger UI at `/api/docs`

### Auth & Security
- **JWT auth** — bcrypt passwords, configurable roles, httpOnly cookies
- **OAuth 2.0** — Google, GitHub, Discord, or any custom OAuth provider
- **Encrypted secrets** — AES-256-GCM for API keys and credentials
- **Rate limiting** — per-IP token bucket (configurable rate and burst)
- **CORS** — configurable per-endpoint

### AI-Powered Endpoints
- **LLM endpoints** — expose the project's LLM provider as API endpoints for chatbots, AI assistants, content generators, and any AI-powered feature
- **Streaming** — `POST /api/{path}/chat` streams responses token-by-token via SSE
- **Non-streaming** — `POST /api/{path}/complete` returns the full response as JSON
- **Server-side system prompts** — define the AI's personality and role, never exposed to the client
- **Rate limiting** — per-IP, configurable per endpoint (default: 10 req/min)
- **Configurable** — max_tokens, temperature, max_history (conversation turn limit), auth requirement

### Real-Time
- **WebSockets** — bidirectional communication with room-based broadcast and echo suppression
- **Server-Sent Events (SSE)** — server-to-client streaming
- **File uploads** — multipart with MIME validation, size limits, optional auto-persist to table

### Payments
- **Checkout flows** — Stripe, PayPal, Mollie, Square, or any REST provider
- **Subscriptions** — create plans, manage recurring billing, trial periods, cancellations
- **Webhook verification** — HMAC-SHA256 signature validation, auto status sync

### Email
- **Transactional email** — SendGrid, Mailgun, Resend, Amazon SES, or any REST provider
- **Email templates** — reusable templates with `{{variable}}` substitution

### Automation
- **Server-side actions** — event-driven triggers (on register, on data insert, on payment, etc.) that fire without the LLM. Send emails, make HTTP requests, insert/update data — all with template variables
- **Scheduled tasks** — cron-based or interval, LLM-driven or native (delete stale rows, run SQL, count, truncate)
- **Background jobs** — async queue with retry, exponential backoff, delayed execution
- **Webhooks** — incoming (HMAC-verified) and outgoing, subscribe to any event type

### Content & SEO
- **Version history** — pages, files, and layouts with full rollback
- **Image processing** — resize, thumbnail, optimize (JPEG/PNG, quality control)
- **SEO toolkit** — meta tags, Open Graph, JSON-LD structured data, canonical URLs, robots directives
- **Sitemap & robots.txt** — auto-generated from published pages
- **SEO validation** — checks all pages for missing essentials
- **PWA support** — generates manifest.json, service-worker.js, and offline page
- **URL redirects** — 301/302 redirects via manage_site

### Analytics & Monitoring
- **Built-in analytics** — page views, unique visitors, top pages, referrers, daily/weekly summaries
- **Diagnostics** — system health (CPU, memory, goroutines, DB stats), integrity checks
- **LLM logging** — token stats per call, CSV export, prompt caching metrics

### Hosting & Deployment
- **Single binary** — no dependencies, embedded admin panel, embedded webserver (Caddy)
- **Free HTTPS** — automatic Let's Encrypt certificates
- **Multi-site** — unlimited projects from one instance, each with isolated database and storage
- **Multi-platform** — Linux (amd64/arm64), macOS (Intel/Apple Silicon), Windows

### Integrations
- **Telegram bot** — built-in long-polling bot for site management and notifications
- **Service providers** — connect any external API with stored credentials and authenticated requests
- **Blob storage** — separate metadata tracking for user uploads with public URL generation

---

## LLM Providers

| Provider | Type | Notes |
|---|---|---|
| Anthropic Claude | anthropic | Sonnet, Haiku, Opus — with prompt caching |
| Any OpenAI-compatible | openai | Ollama (local/free), Z.ai, OpenRouter, Groq, Gemini, etc. |

Providers are configured through the setup wizard or `seed.json`.

---

## Configuration

HO works with zero configuration out of the box. Optionally use `config.json` or environment variables with the `HO_` prefix:

| Variable | Default | Description |
|---|---|---|
| `HO_ADMIN_PORT` | 5001 | Admin panel port |
| `HO_PUBLIC_PORT` | 5000 | Public site port |
| `HO_DATA_DIR` | ./data | Data directory |
| `HO_CADDY_ENABLED` | false | Enable embedded Caddy for HTTPS |
| `HO_CORS_ORIGINS` | | Comma-separated allowed origins |
| `HO_RATE_LIMIT_RATE` | | Requests per second per IP |
| `HO_RATE_LIMIT_BURST` | | Burst size for rate limiter |
| `HO_LOG_LEVEL` | info | Log level (debug, info, warn, error) |
| `HO_LLM_TIMEOUT_SEC` | 420 | LLM call timeout |

JWT secrets and encryption keys are auto-generated on first run.

---

## Production HTTPS

Point your domain's DNS to your server, set the domain in the admin panel, set `HO_CADDY_ENABLED=true`, and restart. Caddy automatically gets a free SSL certificate from Let's Encrypt. Make sure ports 80 and 443 are open.

---

## Build From Source

```bash
# Requires Go 1.25+
git clone https://github.com/markdr-hue/HO.git
cd HO

make build          # Build for your platform
make build-linux    # Linux AMD64 + ARM64
make build-darwin   # macOS Intel + Apple Silicon
make build-windows  # Windows AMD64
make build-all      # Cross-compile everything
make dev            # Run in development mode
```

---

## Security

The admin panel runs on port **5001**. If your server is publicly accessible, block this port externally and use an SSH tunnel (`ssh -L 5001:localhost:5001 user@yourserver`) to access it securely.

---

## Warning

- This is for **testing and experimentation**. Not production. Probably.
- There will be errors, trust me
- HO is not responsible for unemployment

---

## Community

- **Discord** : https://discord.gg/VRdYgDQ2qr
- **X / Twitter** : https://x.com/humans_out_mark
- **GitHub** : https://github.com/markdr-hue/HO

---

## Standing on the Shoulders of Giants

HO's seamless hosting and HTTPS experience wouldn't exist without these two incredible projects:

- **[Caddy](https://caddyserver.com/)** The web server powering this HO. Zero config, zero hassle, just works.
- **[Let's Encrypt](https://letsencrypt.org/)** Free SSL certificates for everyone, thank you.

These projects embody the same spirit HO strives for: powerful things should be simple. Thank you for making the internet a better place.

---

## About Me

- Full-time single dad of 3, they give me purpose.
- My ideas and perspective tend to be a bit unconventional, which means I'm often misunderstood or out of step with the people around me.
- I frequently question whether I see the world differently from everyone else. I've made peace with the fact that the answer is probably yes.

---

## License

MIT
