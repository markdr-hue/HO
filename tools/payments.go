/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package tools

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// PaymentsTool — manage_payments
// ---------------------------------------------------------------------------

// PaymentsTool provides generic payment flow management that works with any
// payment provider (Stripe, PayPal, Mollie, Square, etc.) via service providers.
type PaymentsTool struct{}

func (t *PaymentsTool) Name() string { return "manage_payments" }
func (t *PaymentsTool) Description() string {
	return "Configure, create checkout sessions, check status, list, handle payment webhooks, and manage subscriptions."
}

func (t *PaymentsTool) Guide() string {
	return `### Payments (manage_payments)
Setup flow: 1) Store API key via manage_secrets, 2) Create service provider via manage_providers, 3) Configure payments via manage_payments(action="configure").
- **configure**: Link a service provider to payments. Set provider_name, provider_type (stripe/paypal/mollie/square/generic), currency (e.g. "usd"), and optionally webhook_secret_name.
- **create_checkout**: Create a checkout session. Provide amount (in cents, e.g. 1999 = $19.99), description, success_url, cancel_url. Optionally: currency override, metadata (JSON), customer_email, line_items. Returns checkout_url for redirect + session_id for tracking.
- **check_status**: Check payment status by session_id. Returns status (pending/completed/failed), amount, metadata.
- **list**: List payment sessions. Optional filters: status, limit, offset. Returns sessions with id, status, amount, created_at.
- **handle_webhook**: Process incoming payment webhooks. Provide raw_body and signature for HMAC verification (if webhook_secret configured). Updates session and subscription status automatically.
- For Stripe: amount is in smallest currency unit (cents for USD). Line items use {name, amount, quantity}.
- Client-side flow: form submit → POST to your API endpoint → create_checkout → redirect user to checkout_url → provider handles payment → redirects to success_url.

### Subscriptions
Subscription flow: 1) Configure payments (above), 2) Create a plan with create_plan, 3) Create subscription via create_subscription → redirect user to checkout URL, 4) Handle webhook for lifecycle events (renewals, cancellations).
- **create_plan**: Create a subscription plan. Required: name, amount (cents), currency. Optional: interval (month/year/week, default "month"), trial_days, metadata. Calls provider API (Stripe/PayPal) and stores locally.
- **list_plans**: List subscription plans from ho_subscription_plans. Optional: is_active filter (default true).
- **create_subscription**: Subscribe a user to a plan. Required: plan_id, success_url, cancel_url. Optional: customer_email, user_id, metadata. Returns checkout_url + subscription_id.
- **cancel_subscription**: Cancel an active subscription. Required: subscription_id. Calls provider cancel API and updates local status.
- **list_subscriptions**: List subscriptions. Optional filters: status, user_id, limit, offset. Includes plan details.
- **check_subscription**: Check current subscription status from provider. Required: subscription_id. Syncs local status with provider.`
}

func (t *PaymentsTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"configure", "create_checkout", "check_status", "list", "handle_webhook", "create_plan", "list_plans", "create_subscription", "cancel_subscription", "list_subscriptions", "check_subscription"},
				"description": "Action to perform",
			},
			"provider_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of the service provider (must exist in manage_providers). For configure.",
			},
			"provider_type": map[string]interface{}{
				"type":        "string",
				"description": "Provider type hint: stripe, paypal, mollie, square, generic. For configure.",
				"enum":        []string{"stripe", "paypal", "mollie", "square", "generic"},
			},
			"currency": map[string]interface{}{
				"type":        "string",
				"description": "Default currency code (e.g. 'usd', 'eur'). For configure and create_plan.",
			},
			"webhook_secret_name": map[string]interface{}{
				"type":        "string",
				"description": "Name of secret storing the webhook signing key. For configure.",
			},
			"amount": map[string]interface{}{
				"type":        "number",
				"description": "Amount in cents (e.g. 1999 = $19.99). For create_checkout and create_plan.",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Payment description. For create_checkout.",
			},
			"success_url": map[string]interface{}{
				"type":        "string",
				"description": "URL to redirect after successful payment. For create_checkout and create_subscription.",
			},
			"cancel_url": map[string]interface{}{
				"type":        "string",
				"description": "URL to redirect on payment cancellation. For create_checkout and create_subscription.",
			},
			"metadata": map[string]interface{}{
				"type":        "object",
				"description": "Custom metadata to attach to the payment or subscription.",
			},
			"payment_id": map[string]interface{}{
				"type":        "number",
				"description": "Local payment ID. For check_status.",
			},
			"external_id": map[string]interface{}{
				"type":        "string",
				"description": "Provider's payment/session ID. For check_status.",
			},
			"webhook_body": map[string]interface{}{
				"type":        "string",
				"description": "Raw webhook body (JSON string). For handle_webhook.",
			},
			"webhook_signature": map[string]interface{}{
				"type":        "string",
				"description": "Webhook signature header value. For handle_webhook.",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Plan name. For create_plan.",
			},
			"interval": map[string]interface{}{
				"type":        "string",
				"description": "Billing interval: month, year, week (default: month). For create_plan.",
			},
			"trial_days": map[string]interface{}{
				"type":        "number",
				"description": "Free trial period in days. For create_plan.",
			},
			"plan_id": map[string]interface{}{
				"type":        "number",
				"description": "Subscription plan ID. For create_subscription.",
			},
			"subscription_id": map[string]interface{}{
				"type":        "number",
				"description": "Subscription ID. For cancel_subscription and check_subscription.",
			},
			"user_id": map[string]interface{}{
				"type":        "number",
				"description": "User ID. For create_subscription and list_subscriptions filter.",
			},
			"customer_email": map[string]interface{}{
				"type":        "string",
				"description": "Customer email. For create_subscription.",
			},
			"status": map[string]interface{}{
				"type":        "string",
				"description": "Filter by status. For list_subscriptions.",
			},
			"limit": map[string]interface{}{
				"type":        "number",
				"description": "Max results to return. For list_subscriptions.",
			},
			"offset": map[string]interface{}{
				"type":        "number",
				"description": "Offset for pagination. For list_subscriptions.",
			},
		},
		"required": []string{},
	}
}

func (t *PaymentsTool) Execute(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	return DispatchAction(ctx, args, map[string]ActionHandler{
		"configure":           t.configure,
		"create_checkout":     t.createCheckout,
		"check_status":        t.checkStatus,
		"list":                t.list,
		"handle_webhook":      t.handleWebhook,
		"create_plan":         t.createPlan,
		"list_plans":          t.listPlans,
		"create_subscription": t.createSubscription,
		"cancel_subscription": t.cancelSubscription,
		"list_subscriptions":  t.listSubscriptions,
		"check_subscription":  t.checkSubscription,
	}, nil)
}

func (t *PaymentsTool) ensureTables(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS payment_config (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		provider_name TEXT NOT NULL,
		provider_type TEXT NOT NULL DEFAULT 'generic',
		currency TEXT NOT NULL DEFAULT 'usd',
		webhook_secret_name TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS payments (
		id INTEGER PRIMARY KEY,
		external_id TEXT DEFAULT '',
		amount INTEGER NOT NULL,
		currency TEXT NOT NULL DEFAULT 'usd',
		status TEXT NOT NULL DEFAULT 'pending',
		description TEXT DEFAULT '',
		metadata TEXT DEFAULT '{}',
		checkout_url TEXT DEFAULT '',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS ho_subscription_plans (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		external_id TEXT DEFAULT '',
		amount INTEGER NOT NULL,
		currency TEXT NOT NULL DEFAULT 'usd',
		interval TEXT NOT NULL DEFAULT 'month',
		trial_days INTEGER DEFAULT 0,
		metadata TEXT DEFAULT '{}',
		is_active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS ho_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		plan_id INTEGER REFERENCES ho_subscription_plans(id),
		external_id TEXT DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active',
		current_period_start DATETIME,
		current_period_end DATETIME,
		canceled_at DATETIME,
		metadata TEXT DEFAULT '{}',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
}

func (t *PaymentsTool) configure(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	providerName, _ := args["provider_name"].(string)
	providerType, _ := args["provider_type"].(string)
	currency, _ := args["currency"].(string)
	webhookSecretName, _ := args["webhook_secret_name"].(string)

	if providerName == "" {
		return &Result{Success: false, Error: "provider_name is required"}, nil
	}
	if providerType == "" {
		providerType = "generic"
	}
	if currency == "" {
		currency = "usd"
	}

	// Verify the provider exists.
	var exists int
	err := ctx.DB.QueryRow("SELECT COUNT(*) FROM ho_service_providers WHERE name = ?", providerName).Scan(&exists)
	if err != nil || exists == 0 {
		return &Result{Success: false, Error: fmt.Sprintf("provider '%s' not found — create it first with manage_providers", providerName)}, nil
	}

	t.ensureTables(ctx.DB)

	_, err = ctx.DB.Exec(
		`INSERT INTO payment_config (id, provider_name, provider_type, currency, webhook_secret_name)
		 VALUES (1, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   provider_name = excluded.provider_name,
		   provider_type = excluded.provider_type,
		   currency = excluded.currency,
		   webhook_secret_name = excluded.webhook_secret_name`,
		providerName, providerType, currency, webhookSecretName,
	)
	if err != nil {
		return nil, fmt.Errorf("configuring payments: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"provider": providerName,
		"type":     providerType,
		"currency": currency,
	}}, nil
}

func (t *PaymentsTool) createCheckout(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	amountFloat, _ := args["amount"].(float64)
	amount := int(amountFloat)
	description, _ := args["description"].(string)
	successURL, _ := args["success_url"].(string)
	cancelURL, _ := args["cancel_url"].(string)
	metadata, _ := args["metadata"].(map[string]interface{})

	if amount <= 0 {
		return &Result{Success: false, Error: "amount (in cents) is required and must be positive"}, nil
	}
	if successURL == "" || cancelURL == "" {
		return &Result{Success: false, Error: "success_url and cancel_url are required"}, nil
	}

	// Load payment config.
	t.ensureTables(ctx.DB)
	var providerName, providerType, currency string
	err := ctx.DB.QueryRow(
		"SELECT provider_name, provider_type, currency FROM payment_config WHERE id = 1",
	).Scan(&providerName, &providerType, &currency)
	if err != nil {
		return &Result{Success: false, Error: "payments not configured — use manage_payments(action='configure') first"}, nil
	}

	// Override currency if specified.
	if cur, ok := args["currency"].(string); ok && cur != "" {
		currency = cur
	}

	metadataJSON := "{}"
	if metadata != nil {
		data, _ := json.Marshal(metadata)
		metadataJSON = string(data)
	}

	// Load provider details.
	var baseURL, authType, authHeader, authPrefix string
	var secretName sql.NullString
	err = ctx.DB.QueryRow(
		`SELECT base_url, auth_type, auth_header, auth_prefix, secret_name
		 FROM ho_service_providers WHERE name = ? AND is_enabled = 1`,
		providerName,
	).Scan(&baseURL, &authType, &authHeader, &authPrefix, &secretName)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("payment provider '%s' not found or disabled", providerName)}, nil
	}

	// Build request based on provider type.
	var reqURL, reqBody, contentType string
	switch providerType {
	case "stripe":
		reqURL = strings.TrimRight(baseURL, "/") + "/v1/checkout/sessions"
		contentType = "application/x-www-form-urlencoded"
		reqBody = fmt.Sprintf("mode=payment&success_url=%s&cancel_url=%s&line_items[0][price_data][currency]=%s&line_items[0][price_data][unit_amount]=%d&line_items[0][price_data][product_data][name]=%s&line_items[0][quantity]=1",
			successURL, cancelURL, currency, amount, description)

	case "paypal":
		reqURL = strings.TrimRight(baseURL, "/") + "/v2/checkout/orders"
		contentType = "application/json"
		amountStr := fmt.Sprintf("%.2f", float64(amount)/100.0)
		payload := map[string]interface{}{
			"intent": "CAPTURE",
			"purchase_units": []map[string]interface{}{
				{
					"amount": map[string]string{
						"currency_code": strings.ToUpper(currency),
						"value":         amountStr,
					},
					"description": description,
				},
			},
			"application_context": map[string]string{
				"return_url": successURL,
				"cancel_url": cancelURL,
			},
		}
		data, _ := json.Marshal(payload)
		reqBody = string(data)

	case "mollie":
		reqURL = strings.TrimRight(baseURL, "/") + "/v2/payments"
		contentType = "application/json"
		amountStr := fmt.Sprintf("%.2f", float64(amount)/100.0)
		payload := map[string]interface{}{
			"amount": map[string]string{
				"currency": strings.ToUpper(currency),
				"value":    amountStr,
			},
			"description": description,
			"redirectUrl": successURL,
			"cancelUrl":   cancelURL,
		}
		data, _ := json.Marshal(payload)
		reqBody = string(data)

	default: // "square", "generic"
		reqURL = strings.TrimRight(baseURL, "/") + "/payments"
		contentType = "application/json"
		payload := map[string]interface{}{
			"amount":      amount,
			"currency":    currency,
			"description": description,
			"success_url": successURL,
			"cancel_url":  cancelURL,
			"metadata":    metadata,
		}
		data, _ := json.Marshal(payload)
		reqBody = string(data)
	}

	// Create HTTP request.
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(reqBody))
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
	}
	req.Header.Set("Content-Type", contentType)

	// Inject auth.
	if authType != "none" && secretName.Valid && secretName.String != "" {
		var encryptedValue string
		err := ctx.DB.QueryRow("SELECT value_encrypted FROM ho_secrets WHERE name = ?", secretName.String).Scan(&encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("secret '%s' not found", secretName.String)}, nil
		}
		if ctx.Encryptor == nil {
			return &Result{Success: false, Error: "encryption not available"}, nil
		}
		secretValue, err := ctx.Encryptor.Decrypt(encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: "failed to decrypt secret"}, nil
		}
		switch authType {
		case "bearer":
			req.Header.Set(authHeader, authPrefix+" "+secretValue)
		case "api_key_header":
			req.Header.Set(authHeader, secretValue)
		case "basic":
			req.Header.Set(authHeader, "Basic "+secretValue)
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("creating checkout: %v", err)}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16384))
	if err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("failed to read payment provider response: %v", err)}, nil
	}

	if resp.StatusCode >= 400 {
		return &Result{Success: false, Error: fmt.Sprintf("provider returned %d: %s", resp.StatusCode, string(respBody))}, nil
	}

	// Parse response to extract checkout URL and external ID.
	var respData map[string]interface{}
	if err := json.Unmarshal(respBody, &respData); err != nil {
		return &Result{Success: false, Error: fmt.Sprintf("invalid JSON response from payment provider: %v", err)}, nil
	}

	checkoutURL, externalID := parseCheckoutResponse(providerType, respData)

	// Store payment locally.
	result, err := ctx.DB.Exec(
		`INSERT INTO payments (external_id, amount, currency, status, description, metadata, checkout_url)
		 VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		externalID, amount, currency, description, metadataJSON, checkoutURL,
	)
	if err != nil {
		return nil, fmt.Errorf("storing payment: %w", err)
	}

	paymentID, _ := result.LastInsertId()

	return &Result{Success: true, Data: map[string]interface{}{
		"payment_id":   paymentID,
		"external_id":  externalID,
		"checkout_url": checkoutURL,
		"amount":       amount,
		"currency":     currency,
	}}, nil
}

func (t *PaymentsTool) checkStatus(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	var status, externalID, currency, description string
	var amount int
	var paymentID int64

	if pid, ok := args["payment_id"].(float64); ok {
		paymentID = int64(pid)
		err := ctx.DB.QueryRow(
			"SELECT status, external_id, amount, currency, description FROM payments WHERE id = ?",
			paymentID,
		).Scan(&status, &externalID, &amount, &currency, &description)
		if err != nil {
			return &Result{Success: false, Error: "payment not found"}, nil
		}
	} else if eid, ok := args["external_id"].(string); ok && eid != "" {
		externalID = eid
		err := ctx.DB.QueryRow(
			"SELECT id, status, amount, currency, description FROM payments WHERE external_id = ?",
			externalID,
		).Scan(&paymentID, &status, &amount, &currency, &description)
		if err != nil {
			return &Result{Success: false, Error: "payment not found"}, nil
		}
	} else {
		return &Result{Success: false, Error: "payment_id or external_id is required"}, nil
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"payment_id":  paymentID,
		"external_id": externalID,
		"status":      status,
		"amount":      amount,
		"currency":    currency,
		"description": description,
	}}, nil
}

func (t *PaymentsTool) list(ctx *ToolContext, _ map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	rows, err := ctx.DB.Query(
		"SELECT id, external_id, amount, currency, status, description, created_at FROM payments ORDER BY created_at DESC LIMIT 50",
	)
	if err != nil {
		return nil, fmt.Errorf("listing payments: %w", err)
	}
	defer rows.Close()

	var payments []map[string]interface{}
	for rows.Next() {
		var id, amount int
		var externalID, currency, status, description string
		var createdAt time.Time
		if err := rows.Scan(&id, &externalID, &amount, &currency, &status, &description, &createdAt); err != nil {
			continue
		}
		payments = append(payments, map[string]interface{}{
			"id":          id,
			"external_id": externalID,
			"amount":      amount,
			"currency":    currency,
			"status":      status,
			"description": description,
			"created_at":  createdAt,
		})
	}

	return &Result{Success: true, Data: payments}, nil
}

func (t *PaymentsTool) handleWebhook(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	body, _ := args["webhook_body"].(string)
	signature, _ := args["webhook_signature"].(string)
	if body == "" {
		return &Result{Success: false, Error: "webhook_body is required"}, nil
	}

	// Load payment config for webhook verification.
	var providerType, webhookSecretName string
	err := ctx.DB.QueryRow(
		"SELECT provider_type, webhook_secret_name FROM payment_config WHERE id = 1",
	).Scan(&providerType, &webhookSecretName)
	if err != nil {
		return &Result{Success: false, Error: "payments not configured"}, nil
	}

	// Verify webhook signature — require it when a secret is configured.
	if webhookSecretName != "" {
		if signature == "" {
			return &Result{Success: false, Error: "webhook_signature is required when webhook secret is configured"}, nil
		}
		if ctx.Encryptor == nil {
			return &Result{Success: false, Error: "encryptor not available for webhook signature verification"}, nil
		}
		var encryptedValue string
		err := ctx.DB.QueryRow("SELECT value_encrypted FROM ho_secrets WHERE name = ?", webhookSecretName).Scan(&encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("webhook secret %q not found", webhookSecretName)}, nil
		}
		secretValue, decErr := ctx.Encryptor.Decrypt(encryptedValue)
		if decErr != nil {
			return &Result{Success: false, Error: "failed to decrypt webhook secret"}, nil
		}
		if !verifyWebhookSignature(providerType, body, signature, secretValue) {
			return &Result{Success: false, Error: "webhook signature verification failed"}, nil
		}
	} else {
		ctx.Logger.Warn("processing payment webhook without signature verification — configure webhook_secret_name for security")
	}

	// Parse webhook body.
	var webhookData map[string]interface{}
	if err := json.Unmarshal([]byte(body), &webhookData); err != nil {
		return &Result{Success: false, Error: "invalid webhook JSON"}, nil
	}

	// Check if this is a subscription lifecycle event.
	if subExtID, subStatus := parseSubscriptionWebhookEvent(providerType, webhookData); subExtID != "" && subStatus != "" {
		_, err = ctx.DB.Exec(
			"UPDATE ho_subscriptions SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE external_id = ?",
			subStatus, subExtID,
		)
		if subStatus == "canceled" {
			ctx.DB.Exec(
				"UPDATE ho_subscriptions SET canceled_at = CURRENT_TIMESTAMP WHERE external_id = ? AND canceled_at IS NULL",
				subExtID,
			)
		}
		// For renewal events (invoice.paid), update the period end.
		if isRenewalEvent(providerType, webhookData) {
			periodEnd := parseRenewalPeriodEnd(providerType, webhookData)
			if !periodEnd.IsZero() {
				ctx.DB.Exec(
					"UPDATE ho_subscriptions SET current_period_start = CURRENT_TIMESTAMP, current_period_end = ? WHERE external_id = ?",
					periodEnd, subExtID,
				)
			}
		}
		return &Result{Success: true, Data: map[string]interface{}{
			"type":        "subscription",
			"external_id": subExtID,
			"status":      subStatus,
		}}, nil
	}

	// Extract payment ID and status based on provider type.
	externalID, newStatus := parseWebhookStatus(providerType, webhookData)

	if externalID == "" || newStatus == "" {
		return &Result{Success: false, Error: "could not extract payment ID or status from webhook"}, nil
	}

	// Update payment status.
	_, err = ctx.DB.Exec(
		"UPDATE payments SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE external_id = ?",
		newStatus, externalID,
	)
	if err != nil {
		return nil, fmt.Errorf("updating payment status: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"type":        "payment",
		"external_id": externalID,
		"status":      newStatus,
	}}, nil
}

// ---------------------------------------------------------------------------
// Subscription action handlers
// ---------------------------------------------------------------------------

// paymentConfig holds the loaded payment configuration.
type paymentConfig struct {
	providerName      string
	providerType      string
	currency          string
	webhookSecretName string
}

// providerInfo holds loaded provider details.
type providerInfo struct {
	baseURL    string
	authType   string
	authHeader string
	authPrefix string
	secretName sql.NullString
}

// loadPaymentConfig loads the payment configuration from the database.
func (t *PaymentsTool) loadPaymentConfig(ctx *ToolContext) (*paymentConfig, *Result) {
	t.ensureTables(ctx.DB)
	var cfg paymentConfig
	err := ctx.DB.QueryRow(
		"SELECT provider_name, provider_type, currency, webhook_secret_name FROM payment_config WHERE id = 1",
	).Scan(&cfg.providerName, &cfg.providerType, &cfg.currency, &cfg.webhookSecretName)
	if err != nil {
		return nil, &Result{Success: false, Error: "payments not configured — use manage_payments(action='configure') first"}
	}
	return &cfg, nil
}

// loadProvider loads the service provider details from the database.
func (t *PaymentsTool) loadProvider(ctx *ToolContext, providerName string) (*providerInfo, *Result) {
	var p providerInfo
	err := ctx.DB.QueryRow(
		`SELECT base_url, auth_type, auth_header, auth_prefix, secret_name
		 FROM ho_service_providers WHERE name = ? AND is_enabled = 1`,
		providerName,
	).Scan(&p.baseURL, &p.authType, &p.authHeader, &p.authPrefix, &p.secretName)
	if err != nil {
		return nil, &Result{Success: false, Error: fmt.Sprintf("payment provider '%s' not found or disabled", providerName)}
	}
	return &p, nil
}

// injectAuth sets the authorization header on an HTTP request using provider credentials.
func (t *PaymentsTool) injectAuth(ctx *ToolContext, req *http.Request, p *providerInfo) *Result {
	if p.authType != "none" && p.secretName.Valid && p.secretName.String != "" {
		var encryptedValue string
		err := ctx.DB.QueryRow("SELECT value_encrypted FROM ho_secrets WHERE name = ?", p.secretName.String).Scan(&encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("secret '%s' not found", p.secretName.String)}
		}
		if ctx.Encryptor == nil {
			return &Result{Success: false, Error: "encryption not available"}
		}
		secretValue, err := ctx.Encryptor.Decrypt(encryptedValue)
		if err != nil {
			return &Result{Success: false, Error: "failed to decrypt secret"}
		}
		switch p.authType {
		case "bearer":
			req.Header.Set(p.authHeader, p.authPrefix+" "+secretValue)
		case "api_key_header":
			req.Header.Set(p.authHeader, secretValue)
		case "basic":
			req.Header.Set(p.authHeader, "Basic "+secretValue)
		}
	}
	return nil
}

func (t *PaymentsTool) createPlan(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	name, errR := RequireString(args, "name")
	if errR != nil {
		return errR, nil
	}
	amountFloat, _ := args["amount"].(float64)
	amount := int(amountFloat)
	if amount <= 0 {
		return &Result{Success: false, Error: "amount (in cents) is required and must be positive"}, nil
	}

	cfg, errR := t.loadPaymentConfig(ctx)
	if errR != nil {
		return errR, nil
	}

	currency := OptionalString(args, "currency", cfg.currency)
	interval := OptionalString(args, "interval", "month")
	trialDays := OptionalInt(args, "trial_days", 0)
	metadata, _ := args["metadata"].(map[string]interface{})

	metadataJSON := "{}"
	if metadata != nil {
		data, _ := json.Marshal(metadata)
		metadataJSON = string(data)
	}

	var externalID string

	switch cfg.providerType {
	case "stripe":
		provider, errR := t.loadProvider(ctx, cfg.providerName)
		if errR != nil {
			return errR, nil
		}
		reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/prices"
		reqBody := fmt.Sprintf("unit_amount=%d&currency=%s&recurring[interval]=%s&product_data[name]=%s",
			amount, currency, interval, name)
		if trialDays > 0 {
			reqBody += fmt.Sprintf("&recurring[trial_period_days]=%d", trialDays)
		}

		req, err := http.NewRequest("POST", reqURL, strings.NewReader(reqBody))
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if authErr := t.injectAuth(ctx, req, provider); authErr != nil {
			return authErr, nil
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("calling Stripe: %v", err)}, nil
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if resp.StatusCode >= 400 {
			return &Result{Success: false, Error: fmt.Sprintf("Stripe returned %d: %s", resp.StatusCode, string(respBody))}, nil
		}
		var respData map[string]interface{}
		if err := json.Unmarshal(respBody, &respData); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid JSON from Stripe: %v", err)}, nil
		}
		externalID, _ = respData["id"].(string)

	case "paypal":
		provider, errR := t.loadProvider(ctx, cfg.providerName)
		if errR != nil {
			return errR, nil
		}
		reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/billing/plans"
		payload := map[string]interface{}{
			"product_id":  name,
			"name":        name,
			"description": name,
			"status":      "ACTIVE",
			"billing_cycles": []map[string]interface{}{
				{
					"frequency": map[string]interface{}{
						"interval_unit":  strings.ToUpper(interval),
						"interval_count": 1,
					},
					"tenure_type":  "REGULAR",
					"sequence":     1,
					"total_cycles": 0,
					"pricing_scheme": map[string]interface{}{
						"fixed_price": map[string]string{
							"value":         fmt.Sprintf("%.2f", float64(amount)/100.0),
							"currency_code": strings.ToUpper(currency),
						},
					},
				},
			},
			"payment_preferences": map[string]interface{}{
				"auto_bill_outstanding": true,
			},
		}
		data, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", reqURL, strings.NewReader(string(data)))
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
		}
		req.Header.Set("Content-Type", "application/json")
		if authErr := t.injectAuth(ctx, req, provider); authErr != nil {
			return authErr, nil
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("calling PayPal: %v", err)}, nil
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if resp.StatusCode >= 400 {
			return &Result{Success: false, Error: fmt.Sprintf("PayPal returned %d: %s", resp.StatusCode, string(respBody))}, nil
		}
		var respData map[string]interface{}
		if err := json.Unmarshal(respBody, &respData); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid JSON from PayPal: %v", err)}, nil
		}
		externalID, _ = respData["id"].(string)

	default:
		// Generic/other: store locally only, no external API call.
	}

	result, err := ctx.DB.Exec(
		`INSERT INTO ho_subscription_plans (name, external_id, amount, currency, interval, trial_days, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		name, externalID, amount, currency, interval, trialDays, metadataJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("storing subscription plan: %w", err)
	}

	planID, _ := result.LastInsertId()

	return &Result{Success: true, Data: map[string]interface{}{
		"plan_id":     planID,
		"name":        name,
		"external_id": externalID,
		"amount":      amount,
		"currency":    currency,
		"interval":    interval,
		"trial_days":  trialDays,
	}}, nil
}

func (t *PaymentsTool) listPlans(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	rows, err := ctx.DB.Query(
		"SELECT id, name, external_id, amount, currency, interval, trial_days, is_active, created_at FROM ho_subscription_plans WHERE is_active = 1 ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("listing plans: %w", err)
	}
	defer rows.Close()

	var plans []map[string]interface{}
	for rows.Next() {
		var id, amount, trialDays int
		var isActive bool
		var name, externalID, currency, interval string
		var createdAt time.Time
		if err := rows.Scan(&id, &name, &externalID, &amount, &currency, &interval, &trialDays, &isActive, &createdAt); err != nil {
			continue
		}
		plans = append(plans, map[string]interface{}{
			"id":          id,
			"name":        name,
			"external_id": externalID,
			"amount":      amount,
			"currency":    currency,
			"interval":    interval,
			"trial_days":  trialDays,
			"is_active":   isActive,
			"created_at":  createdAt,
		})
	}

	return &Result{Success: true, Data: plans}, nil
}

func (t *PaymentsTool) createSubscription(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	planIDFloat, _ := args["plan_id"].(float64)
	planID := int(planIDFloat)
	if planID <= 0 {
		return &Result{Success: false, Error: "plan_id is required"}, nil
	}
	successURL, errR := RequireString(args, "success_url")
	if errR != nil {
		return errR, nil
	}
	cancelURL, errR := RequireString(args, "cancel_url")
	if errR != nil {
		return errR, nil
	}
	customerEmail := OptionalString(args, "customer_email", "")
	userID := OptionalInt(args, "user_id", 0)
	metadata, _ := args["metadata"].(map[string]interface{})

	metadataJSON := "{}"
	if metadata != nil {
		data, _ := json.Marshal(metadata)
		metadataJSON = string(data)
	}

	// Load plan.
	t.ensureTables(ctx.DB)
	var planName, planExternalID, planCurrency, planInterval string
	var planAmount, planTrialDays int
	err := ctx.DB.QueryRow(
		"SELECT name, external_id, amount, currency, interval, trial_days FROM ho_subscription_plans WHERE id = ? AND is_active = 1",
		planID,
	).Scan(&planName, &planExternalID, &planAmount, &planCurrency, &planInterval, &planTrialDays)
	if err != nil {
		return &Result{Success: false, Error: "plan not found or inactive"}, nil
	}

	cfg, errR := t.loadPaymentConfig(ctx)
	if errR != nil {
		return errR, nil
	}

	var checkoutURL, externalID string

	switch cfg.providerType {
	case "stripe":
		provider, errR := t.loadProvider(ctx, cfg.providerName)
		if errR != nil {
			return errR, nil
		}
		if planExternalID == "" {
			return &Result{Success: false, Error: "plan has no external_id — recreate the plan with a Stripe-configured provider"}, nil
		}
		reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/checkout/sessions"
		reqBody := fmt.Sprintf("mode=subscription&line_items[0][price]=%s&line_items[0][quantity]=1&success_url=%s&cancel_url=%s",
			planExternalID, successURL, cancelURL)
		if customerEmail != "" {
			reqBody += "&customer_email=" + customerEmail
		}
		if planTrialDays > 0 {
			reqBody += fmt.Sprintf("&subscription_data[trial_period_days]=%d", planTrialDays)
		}

		req, err := http.NewRequest("POST", reqURL, strings.NewReader(reqBody))
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if authErr := t.injectAuth(ctx, req, provider); authErr != nil {
			return authErr, nil
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("calling Stripe: %v", err)}, nil
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if resp.StatusCode >= 400 {
			return &Result{Success: false, Error: fmt.Sprintf("Stripe returned %d: %s", resp.StatusCode, string(respBody))}, nil
		}
		var respData map[string]interface{}
		if err := json.Unmarshal(respBody, &respData); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid JSON from Stripe: %v", err)}, nil
		}
		checkoutURL, _ = respData["url"].(string)
		externalID, _ = respData["subscription"].(string)
		if externalID == "" {
			externalID, _ = respData["id"].(string)
		}

	case "paypal":
		provider, errR := t.loadProvider(ctx, cfg.providerName)
		if errR != nil {
			return errR, nil
		}
		if planExternalID == "" {
			return &Result{Success: false, Error: "plan has no external_id — recreate the plan with a PayPal-configured provider"}, nil
		}
		reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/billing/subscriptions"
		payload := map[string]interface{}{
			"plan_id": planExternalID,
			"application_context": map[string]string{
				"return_url": successURL,
				"cancel_url": cancelURL,
			},
		}
		if customerEmail != "" {
			payload["subscriber"] = map[string]interface{}{
				"email_address": customerEmail,
			}
		}
		data, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", reqURL, strings.NewReader(string(data)))
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("creating request: %v", err)}, nil
		}
		req.Header.Set("Content-Type", "application/json")
		if authErr := t.injectAuth(ctx, req, provider); authErr != nil {
			return authErr, nil
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("calling PayPal: %v", err)}, nil
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
		if resp.StatusCode >= 400 {
			return &Result{Success: false, Error: fmt.Sprintf("PayPal returned %d: %s", resp.StatusCode, string(respBody))}, nil
		}
		var respData map[string]interface{}
		if err := json.Unmarshal(respBody, &respData); err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("invalid JSON from PayPal: %v", err)}, nil
		}
		externalID, _ = respData["id"].(string)
		if links, ok := respData["links"].([]interface{}); ok {
			for _, l := range links {
				if link, ok := l.(map[string]interface{}); ok {
					if rel, _ := link["rel"].(string); rel == "approve" {
						checkoutURL, _ = link["href"].(string)
					}
				}
			}
		}

	default:
		return &Result{Success: false, Error: "subscriptions require a supported provider (stripe, paypal)"}, nil
	}

	// Store subscription locally.
	now := time.Now()
	var userIDVal interface{}
	if userID > 0 {
		userIDVal = userID
	}
	result, err := ctx.DB.Exec(
		`INSERT INTO ho_subscriptions (user_id, plan_id, external_id, status, current_period_start, metadata)
		 VALUES (?, ?, ?, 'pending', ?, ?)`,
		userIDVal, planID, externalID, now, metadataJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("storing subscription: %w", err)
	}

	subscriptionID, _ := result.LastInsertId()

	return &Result{Success: true, Data: map[string]interface{}{
		"subscription_id": subscriptionID,
		"external_id":     externalID,
		"checkout_url":    checkoutURL,
		"plan_id":         planID,
		"status":          "pending",
	}}, nil
}

func (t *PaymentsTool) cancelSubscription(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	subIDFloat, _ := args["subscription_id"].(float64)
	subID := int(subIDFloat)
	if subID <= 0 {
		return &Result{Success: false, Error: "subscription_id is required"}, nil
	}

	t.ensureTables(ctx.DB)

	var externalID, status string
	err := ctx.DB.QueryRow(
		"SELECT external_id, status FROM ho_subscriptions WHERE id = ?", subID,
	).Scan(&externalID, &status)
	if err != nil {
		return &Result{Success: false, Error: "subscription not found"}, nil
	}
	if status == "canceled" {
		return &Result{Success: false, Error: "subscription is already canceled"}, nil
	}

	cfg, errR := t.loadPaymentConfig(ctx)
	if errR != nil {
		return errR, nil
	}

	if externalID != "" {
		provider, errR := t.loadProvider(ctx, cfg.providerName)
		if errR != nil {
			return errR, nil
		}

		var req *http.Request
		switch cfg.providerType {
		case "stripe":
			reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/subscriptions/" + externalID
			req, err = http.NewRequest("DELETE", reqURL, nil)
		case "paypal":
			reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/billing/subscriptions/" + externalID + "/cancel"
			payload := `{"reason":"Canceled by user"}`
			req, err = http.NewRequest("POST", reqURL, strings.NewReader(payload))
			if req != nil {
				req.Header.Set("Content-Type", "application/json")
			}
		}

		if err != nil {
			return &Result{Success: false, Error: fmt.Sprintf("creating cancel request: %v", err)}, nil
		}
		if req != nil {
			if authErr := t.injectAuth(ctx, req, provider); authErr != nil {
				return authErr, nil
			}
			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return &Result{Success: false, Error: fmt.Sprintf("calling provider cancel API: %v", err)}, nil
			}
			defer resp.Body.Close()
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
			if resp.StatusCode >= 400 {
				return &Result{Success: false, Error: fmt.Sprintf("provider returned %d: %s", resp.StatusCode, string(respBody))}, nil
			}
		}
	}

	// Update local status.
	_, err = ctx.DB.Exec(
		"UPDATE ho_subscriptions SET status = 'canceled', canceled_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		subID,
	)
	if err != nil {
		return nil, fmt.Errorf("updating subscription: %w", err)
	}

	return &Result{Success: true, Data: map[string]interface{}{
		"subscription_id": subID,
		"status":          "canceled",
	}}, nil
}

func (t *PaymentsTool) listSubscriptions(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	t.ensureTables(ctx.DB)

	statusFilter := OptionalString(args, "status", "")
	userID := OptionalInt(args, "user_id", 0)
	limit := OptionalInt(args, "limit", 50)
	offset := OptionalInt(args, "offset", 0)

	query := `SELECT s.id, s.user_id, s.plan_id, s.external_id, s.status,
		s.current_period_start, s.current_period_end, s.canceled_at, s.created_at,
		p.name, p.amount, p.currency, p.interval
		FROM ho_subscriptions s
		LEFT JOIN ho_subscription_plans p ON s.plan_id = p.id
		WHERE 1=1`
	var queryArgs []interface{}

	if statusFilter != "" {
		query += " AND s.status = ?"
		queryArgs = append(queryArgs, statusFilter)
	}
	if userID > 0 {
		query += " AND s.user_id = ?"
		queryArgs = append(queryArgs, userID)
	}
	query += " ORDER BY s.created_at DESC LIMIT ? OFFSET ?"
	queryArgs = append(queryArgs, limit, offset)

	rows, err := ctx.DB.Query(query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing subscriptions: %w", err)
	}
	defer rows.Close()

	var subs []map[string]interface{}
	for rows.Next() {
		var id, planID int
		var userIDVal sql.NullInt64
		var externalID, status string
		var periodStart, periodEnd, canceledAt sql.NullTime
		var createdAt time.Time
		var planName sql.NullString
		var planAmount sql.NullInt64
		var planCurrency, planInterval sql.NullString

		if err := rows.Scan(&id, &userIDVal, &planID, &externalID, &status,
			&periodStart, &periodEnd, &canceledAt, &createdAt,
			&planName, &planAmount, &planCurrency, &planInterval); err != nil {
			continue
		}

		sub := map[string]interface{}{
			"id":          id,
			"plan_id":     planID,
			"external_id": externalID,
			"status":      status,
			"created_at":  createdAt,
		}
		if userIDVal.Valid {
			sub["user_id"] = userIDVal.Int64
		}
		if periodStart.Valid {
			sub["current_period_start"] = periodStart.Time
		}
		if periodEnd.Valid {
			sub["current_period_end"] = periodEnd.Time
		}
		if canceledAt.Valid {
			sub["canceled_at"] = canceledAt.Time
		}
		if planName.Valid {
			sub["plan_name"] = planName.String
			sub["plan_amount"] = planAmount.Int64
			sub["plan_currency"] = planCurrency.String
			sub["plan_interval"] = planInterval.String
		}
		subs = append(subs, sub)
	}

	return &Result{Success: true, Data: subs}, nil
}

func (t *PaymentsTool) checkSubscription(ctx *ToolContext, args map[string]interface{}) (*Result, error) {
	subIDFloat, _ := args["subscription_id"].(float64)
	subID := int(subIDFloat)
	if subID <= 0 {
		return &Result{Success: false, Error: "subscription_id is required"}, nil
	}

	t.ensureTables(ctx.DB)

	var externalID, status string
	var periodStart, periodEnd, canceledAt sql.NullTime
	var planID int
	err := ctx.DB.QueryRow(
		"SELECT external_id, status, plan_id, current_period_start, current_period_end, canceled_at FROM ho_subscriptions WHERE id = ?",
		subID,
	).Scan(&externalID, &status, &planID, &periodStart, &periodEnd, &canceledAt)
	if err != nil {
		return &Result{Success: false, Error: "subscription not found"}, nil
	}

	// If we have an external_id, query the provider for current status.
	if externalID != "" {
		cfg, errR := t.loadPaymentConfig(ctx)
		if errR == nil {
			provider, errR := t.loadProvider(ctx, cfg.providerName)
			if errR == nil {
				var req *http.Request
				var reqErr error
				switch cfg.providerType {
				case "stripe":
					reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/subscriptions/" + externalID
					req, reqErr = http.NewRequest("GET", reqURL, nil)
				case "paypal":
					reqURL := strings.TrimRight(provider.baseURL, "/") + "/v1/billing/subscriptions/" + externalID
					req, reqErr = http.NewRequest("GET", reqURL, nil)
				}
				if reqErr == nil && req != nil {
					if authErr := t.injectAuth(ctx, req, provider); authErr == nil {
						client := &http.Client{Timeout: 30 * time.Second}
						resp, err := client.Do(req)
						if err == nil {
							defer resp.Body.Close()
							respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
							if resp.StatusCode < 400 {
								var respData map[string]interface{}
								if json.Unmarshal(respBody, &respData) == nil {
									newStatus := parseSubscriptionStatus(cfg.providerType, respData)
									if newStatus != "" && newStatus != status {
										status = newStatus
										ctx.DB.Exec(
											"UPDATE ho_subscriptions SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
											status, subID,
										)
									}
									// Update period info from provider.
									pStart, pEnd := parseSubscriptionPeriod(cfg.providerType, respData)
									if !pStart.IsZero() {
										ctx.DB.Exec(
											"UPDATE ho_subscriptions SET current_period_start = ?, current_period_end = ? WHERE id = ?",
											pStart, pEnd, subID,
										)
										periodStart = sql.NullTime{Time: pStart, Valid: true}
										periodEnd = sql.NullTime{Time: pEnd, Valid: true}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	result := map[string]interface{}{
		"subscription_id": subID,
		"external_id":     externalID,
		"status":          status,
		"plan_id":         planID,
	}
	if periodStart.Valid {
		result["current_period_start"] = periodStart.Time
	}
	if periodEnd.Valid {
		result["current_period_end"] = periodEnd.Time
	}
	if canceledAt.Valid {
		result["canceled_at"] = canceledAt.Time
	}

	return &Result{Success: true, Data: result}, nil
}

// parseSubscriptionStatus extracts the normalized subscription status from a provider response.
func parseSubscriptionStatus(providerType string, data map[string]interface{}) string {
	switch providerType {
	case "stripe":
		if s, ok := data["status"].(string); ok {
			switch s {
			case "active":
				return "active"
			case "past_due":
				return "past_due"
			case "canceled":
				return "canceled"
			case "trialing":
				return "trialing"
			default:
				return s
			}
		}
	case "paypal":
		if s, ok := data["status"].(string); ok {
			switch s {
			case "ACTIVE":
				return "active"
			case "CANCELLED":
				return "canceled"
			case "EXPIRED":
				return "canceled"
			case "SUSPENDED":
				return "past_due"
			default:
				return strings.ToLower(s)
			}
		}
	}
	return ""
}

// parseSubscriptionPeriod extracts the current billing period from a provider response.
func parseSubscriptionPeriod(providerType string, data map[string]interface{}) (start, end time.Time) {
	switch providerType {
	case "stripe":
		if ps, ok := data["current_period_start"].(float64); ok {
			start = time.Unix(int64(ps), 0)
		}
		if pe, ok := data["current_period_end"].(float64); ok {
			end = time.Unix(int64(pe), 0)
		}
	case "paypal":
		if bi, ok := data["billing_info"].(map[string]interface{}); ok {
			if nextDate, ok := bi["next_billing_time"].(string); ok {
				if t, err := time.Parse(time.RFC3339, nextDate); err == nil {
					end = t
				}
			}
		}
		if startTime, ok := data["start_time"].(string); ok {
			if t, err := time.Parse(time.RFC3339, startTime); err == nil {
				start = t
			}
		}
	}
	return
}

// verifyWebhookSignature performs HMAC-SHA256 verification for webhook signatures.
func verifyWebhookSignature(providerType, body, signature, secret string) bool {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	// Different providers format signatures differently.
	switch providerType {
	case "stripe":
		// Stripe uses "t=timestamp,v1=signature" format — extract v1.
		for _, part := range strings.Split(signature, ",") {
			if strings.HasPrefix(part, "v1=") {
				return hmac.Equal([]byte(part[3:]), []byte(expectedMAC))
			}
		}
		return false
	default:
		// Generic: compare directly.
		return hmac.Equal([]byte(signature), []byte(expectedMAC))
	}
}

// parseSubscriptionWebhookEvent checks whether the webhook is a subscription
// lifecycle event and returns the subscription external ID and new status.
func parseSubscriptionWebhookEvent(providerType string, data map[string]interface{}) (externalID, status string) {
	switch providerType {
	case "stripe":
		eventType, _ := data["type"].(string)
		obj, _ := data["data"].(map[string]interface{})
		if obj == nil {
			return
		}
		inner, _ := obj["object"].(map[string]interface{})
		if inner == nil {
			return
		}
		switch eventType {
		case "invoice.paid":
			// Renewal: extract subscription ID from the invoice.
			externalID, _ = inner["subscription"].(string)
			status = "active"
		case "customer.subscription.deleted":
			externalID, _ = inner["id"].(string)
			status = "canceled"
		case "customer.subscription.updated":
			externalID, _ = inner["id"].(string)
			if s, ok := inner["status"].(string); ok {
				status = s
			}
		default:
			return "", ""
		}
	case "paypal":
		eventType, _ := data["event_type"].(string)
		resource, _ := data["resource"].(map[string]interface{})
		if resource == nil {
			return
		}
		switch eventType {
		case "BILLING.SUBSCRIPTION.ACTIVATED":
			externalID, _ = resource["id"].(string)
			status = "active"
		case "BILLING.SUBSCRIPTION.CANCELLED":
			externalID, _ = resource["id"].(string)
			status = "canceled"
		case "BILLING.SUBSCRIPTION.EXPIRED":
			externalID, _ = resource["id"].(string)
			status = "canceled"
		default:
			return "", ""
		}
	}
	return
}

// isRenewalEvent returns true if the webhook event represents a subscription renewal.
func isRenewalEvent(providerType string, data map[string]interface{}) bool {
	switch providerType {
	case "stripe":
		eventType, _ := data["type"].(string)
		return eventType == "invoice.paid"
	case "paypal":
		eventType, _ := data["event_type"].(string)
		return eventType == "BILLING.SUBSCRIPTION.ACTIVATED"
	}
	return false
}

// parseRenewalPeriodEnd extracts the next period end time from a renewal webhook event.
func parseRenewalPeriodEnd(providerType string, data map[string]interface{}) time.Time {
	switch providerType {
	case "stripe":
		if obj, ok := data["data"].(map[string]interface{}); ok {
			if inner, ok := obj["object"].(map[string]interface{}); ok {
				if lines, ok := inner["lines"].(map[string]interface{}); ok {
					if lineData, ok := lines["data"].([]interface{}); ok && len(lineData) > 0 {
						if line, ok := lineData[0].(map[string]interface{}); ok {
							if pe, ok := line["period"].(map[string]interface{}); ok {
								if end, ok := pe["end"].(float64); ok {
									return time.Unix(int64(end), 0)
								}
							}
						}
					}
				}
			}
		}
	}
	return time.Time{}
}

func (t *PaymentsTool) Summarize(result string) string {
	r, dataMap, dataArr, ok := parseSummaryResult(result)
	if !ok {
		return summarizeTruncate(result, 200)
	}
	if !r.Success {
		return summarizeError(r.Error)
	}
	if dataArr != nil {
		return fmt.Sprintf(`{"success":true,"summary":"Listed %d items"}`, len(dataArr))
	}
	if planID, ok := dataMap["plan_id"]; ok {
		if name, _ := dataMap["name"].(string); name != "" {
			return fmt.Sprintf(`{"success":true,"summary":"Plan '%s' (ID %v) created"}`, name, planID)
		}
	}
	if subID, ok := dataMap["subscription_id"]; ok {
		if status, _ := dataMap["status"].(string); status != "" {
			return fmt.Sprintf(`{"success":true,"summary":"Subscription %v: %s"}`, subID, status)
		}
	}
	if status, _ := dataMap["status"].(string); status != "" {
		return fmt.Sprintf(`{"success":true,"summary":"Status: %s"}`, status)
	}
	return summarizeTruncate(result, 300)
}

// parseCheckoutResponse extracts the checkout URL and external ID from a
// payment provider's checkout creation response.
func parseCheckoutResponse(providerType string, data map[string]interface{}) (checkoutURL, externalID string) {
	switch providerType {
	case "stripe":
		checkoutURL, _ = data["url"].(string)
		externalID, _ = data["id"].(string)
	case "paypal":
		externalID, _ = data["id"].(string)
		if links, ok := data["links"].([]interface{}); ok {
			for _, l := range links {
				if link, ok := l.(map[string]interface{}); ok {
					if rel, _ := link["rel"].(string); rel == "approve" {
						checkoutURL, _ = link["href"].(string)
					}
				}
			}
		}
	case "mollie":
		externalID, _ = data["id"].(string)
		if links, ok := data["_links"].(map[string]interface{}); ok {
			if checkout, ok := links["checkout"].(map[string]interface{}); ok {
				checkoutURL, _ = checkout["href"].(string)
			}
		}
	default:
		checkoutURL, _ = data["checkout_url"].(string)
		if checkoutURL == "" {
			checkoutURL, _ = data["url"].(string)
		}
		externalID, _ = data["id"].(string)
		if externalID == "" {
			externalID, _ = data["payment_id"].(string)
		}
	}
	return
}

// parseWebhookStatus extracts the external payment ID and normalized status
// from a payment provider's webhook payload.
func parseWebhookStatus(providerType string, data map[string]interface{}) (externalID, status string) {
	switch providerType {
	case "stripe":
		if obj, ok := data["data"].(map[string]interface{}); ok {
			if inner, ok := obj["object"].(map[string]interface{}); ok {
				externalID, _ = inner["id"].(string)
				if paymentStatus, ok := inner["payment_status"].(string); ok {
					if paymentStatus == "paid" {
						status = "paid"
					} else {
						status = paymentStatus
					}
				}
			}
		}
	case "paypal":
		if resource, ok := data["resource"].(map[string]interface{}); ok {
			externalID, _ = resource["id"].(string)
			if s, ok := resource["status"].(string); ok {
				if s == "COMPLETED" {
					status = "paid"
				} else {
					status = strings.ToLower(s)
				}
			}
		}
	case "mollie":
		externalID, _ = data["id"].(string)
		if s, ok := data["status"].(string); ok {
			status = s
		}
	default:
		externalID, _ = data["id"].(string)
		if externalID == "" {
			externalID, _ = data["payment_id"].(string)
		}
		status, _ = data["status"].(string)
	}
	return
}
