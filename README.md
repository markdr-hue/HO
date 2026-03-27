# HO (Humans. Out.)

**Describe what you want. She builds it. Instantly hosted. All on your machine.**

HO will builds your webapps, sites and games from a simple description. No coding, no hosting providers, no monthly fees. Just tell her what you want.

So simple your grandma could launch her multiplayer Bridge game before lunch.

> ### See her in action
> [From nothing to a live, self-hosted chatbot with attitude in 12 minutes](https://www.youtube.com/watch?v=zdmYUoBI8rQ)

![HO Screenshot 1](readme_assets/screen_1.jpg)

![HO Screenshot 2](readme_assets/screen_2.jpg)

---

## Get Started in 2 Minutes

1. **Download** from [Releases](https://github.com/markdr-hue/HO/releases)
2. **Run it.** Double-click the file (or run `./ho` in terminal)
3. **Open** `http://localhost:5001` and the setup wizard walks you through the rest
4. **Describe your project.** HO takes it from there

Your project is live at `http://localhost:5000`. That's it.

---

## What Can She Build?

Anything running in a browser. Portfolios, dashboards, complete CMS's, marketplaces, multiplayer games, SaaS tools, blogs, chat apps, internal tools...whatever.

HO doesn't just generate simple HTML and CSS. She builds the whole thing: database, backend, frontend, APIs, user accounts, payments, real-time features, and keeps it all running.

---

## How She Works

You describe what you want. HO does the rest.

She **plans** your project's pages, data, features, and design. If something's unclear or she needs input like API keys, she asks you first.

Then she **builds** everything in one go: database, API, pages, styles, interactivity. After that she **checks her own work**, verifies everything was built correctly, and fixes what's missing.

Once live, she **monitors** your project every few minutes and self-heals issues she finds. When you want changes, just tell her via chat. Only the affected parts get rebuilt.

If HO crashes or restarts, she picks up exactly where she left off. She remembers your preferences across conversations.

---

## What's Included

### User Accounts & Security
Let visitors create accounts, log in, and see only their own data. Passwords are automatically encrypted. Add social login with Google, GitHub, or Discord. Control who can access what with roles like "admin" or "editor."

### Databases
Every project gets its own database. HO creates tables and columns based on your description. Supports encrypted fields, linked tables, full-text search, and bulk import/export.

### APIs
Every table becomes a ready-to-use API automatically. Create, read, update, delete with filtering, sorting, and pagination. Rate limiting, access control, caching, and auto-generated documentation included.

### AI Features
Give your project its own AI. Chatbots, writing assistants, content generators. Real-time streaming responses or one-shot completions. Configure personality, memory, and rate limits per feature.

### Real-Time
Live communication between your app and its users. Chat rooms, notifications, live feeds, file uploads...No page refresh needed.

### Payments
Accept payments with Stripe, PayPal, Mollie, or Square. One-time charges or subscriptions. Payment confirmations are cryptographically verified.

### Email
Send welcome messages, confirmations, notifications, and receipts. Works with SendGrid, Mailgun, Resend, Amazon SES, or any email service. Design reusable templates.

### Automation
Set up rules: when a user registers → send a welcome email. When an order is placed → notify the team. Schedule tasks to run hourly, daily, or on any schedule. Queue background work with automatic retry.

### Pages & Content
Version history on every change with full rollback. Reusable components, dynamic pages, image processing, and one-click PWA setup so your project works like a phone app.

### SEO
Meta tags, social sharing previews, structured data, sitemaps, and robots.txt. All managed automatically so search engines find you.

### Analytics
See who's visiting, what's popular, and whether anything's broken. Track AI usage and costs.

### Hosting
One file, runs anywhere. No Docker, no database server, no web server to configure. Point your domain and get free HTTPS automatically. Run unlimited projects from one instance.

### Integrations
Manage projects from Telegram. Connect external APIs. Receive and send webhooks.

---

## LLM Providers

HO works with multiple AI providers:

| Provider | Notes |
|---|---|
| **Anthropic Claude** | Sonnet, Haiku, Opus. Smart caching to reduce costs |
| **Any OpenAI-compatible** | Ollama (local/free), OpenRouter, Groq, Gemini, and more |

Set up through the wizard or a config file. Bring your own API key.

---

## Self-Hosting & HTTPS

Point your domain's DNS to your server, enable HTTPS in settings, restart. Free SSL certificates are provisioned automatically. Make sure ports 80 and 443 are open so Let's Encrypt can do the validation correctly.

---

## Configuration

HO works with zero configuration. Optionally tweak via `config.json` or environment variables:

| Variable | Default | What it does |
|---|---|---|
| `HO_ADMIN_PORT` | 5001 | Port for the admin panel |
| `HO_PUBLIC_PORT` | 5000 | Port where your project is served |
| `HO_DATA_DIR` | ./data | Where project data is stored |
| `HO_CADDY_ENABLED` | false | Enable automatic HTTPS |
| `HO_CORS_ORIGINS` | | Which external sites can call your APIs |
| `HO_LOG_LEVEL` | info | How much detail to log |

Security keys are auto-generated on first run.

---

## Build From Source

```bash
# Requires Go 1.25+
git clone https://github.com/markdr-hue/HO.git && cd HO
make build      # Your platform
make build-all  # Cross-compile everything
make dev        # Development mode
```

---

## Security

The admin panel runs on port **5001**. If your server is publicly accessible, block this port externally and use an SSH tunnel (`ssh -L 5001:localhost:5001 user@yourserver`) to access it securely.

---

## Warning

This is for **testing and experimentation**. Not production. Probably. There will be errors, trust me. HO is not responsible for unemployment.

---

## Community

**Discord** https://discord.gg/VRdYgDQ2qr
**X / Twitter** https://x.com/humans_out_mark
**GitHub** https://github.com/markdr-hue/HO

---

## Special Thanks

**[Caddy](https://caddyserver.com/)** Zero-config web server powering HO's hosting.
**[Let's Encrypt](https://letsencrypt.org/)** Free SSL certificates for everyone.

These projects embody the same spirit: powerful things should be simple.

---

## About Me

Full-time single dad of 3 with a full-time job to match. If you have mental problems try to seek help. Don't suffer in silence. Be yourself, don't let anyone decide what to do or think. No need to idolize 'successful' people, politicians or companies, most of them are clueless.

---

## License

MIT
