# Runs SwiftDrop against Stripe TEST mode, with a webhook listener that matches it.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\stripe-dev.ps1
#
# What it does, so nothing has to be copied by hand:
#   1. asks for your Stripe secret key (hidden while you type; it is never written to disk)
#   2. refuses anything that is not a TEST key (sk_test_...), so live money cannot be touched
#   3. asks the Stripe CLI for the webhook signing secret of THAT key's account, so the
#      listener and the server always agree (a listener signed in to another account, or a
#      secret from an earlier session, is the usual reason a webhook is rejected)
#   4. opens the listener (stripe listen) in a new window
#   5. starts the server here, with STRIPE_KEY and STRIPE_WEBHOOK_SECRET set
#
# Needs the Stripe CLI (winget install Stripe.StripeCli) and the local Postgres running.

$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

$secure = Read-Host 'Stripe TEST secret key (sk_test_...)' -AsSecureString
$key = [System.Net.NetworkCredential]::new('', $secure).Password
if (-not $key.StartsWith('sk_test_')) {
    throw 'That is not a test-mode secret key (it must start with sk_test_). Nothing was started.'
}

$secret = (stripe listen --api-key $key --print-secret | Select-Object -Last 1).Trim()
if (-not $secret.StartsWith('whsec_')) {
    throw "The Stripe CLI did not return a webhook signing secret: $secret"
}

$env:STRIPE_KEY = $key
$env:STRIPE_WEBHOOK_SECRET = $secret
Write-Host 'Key accepted and webhook secret fetched for the same account.'

# The listener window inherits the key from this one, so it is never typed or printed.
$events = 'payment_intent.succeeded,payment_intent.payment_failed,refund.failed,refund.updated'
Start-Process powershell -ArgumentList '-NoExit', '-Command',
    "stripe listen --api-key `$env:STRIPE_KEY --events $events --forward-to localhost:8080/webhooks/stripe"

Write-Host 'Listener opened in a new window. Starting the server (Ctrl+C stops it).'
go run ./cmd/api
