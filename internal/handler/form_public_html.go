package handler

import (
	"fmt"
	"html"
)

// The forms module serves four small pages straight from the backend: the
// confirmation button a visitor lands on from an email, the two outcomes of
// pressing it, and the hosted page that renders a form on its own URL. They
// are hand-written strings rather than templates — four pages do not justify a
// template engine, and keeping them here means every dynamic value passes
// through html.EscapeString on the line that inserts it.

// formPageContentType is what all of them are served as. These responses
// deliberately bypass the API envelope: their audience is a browser window,
// not a script.
const formPageContentType = "text/html; charset=utf-8"

// formPageCSS is inlined into every page. No external stylesheet, no font
// download, no third-party request: these pages must render identically for a
// visitor who has never heard of this CRM and whose network may block anything
// unexpected.
const formPageCSS = `*, *::before, *::after { box-sizing: border-box; }
body {
  margin: 0;
  min-height: 100vh;
  padding: 32px 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f4f5f7;
  color: #1f2430;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  font-size: 16px;
  line-height: 1.55;
}
body.form-page { align-items: flex-start; }
main {
  width: 100%;
  max-width: 32rem;
}
main.card {
  background: #ffffff;
  padding: 32px;
  border-radius: 10px;
  box-shadow: 0 1px 2px rgba(16, 24, 40, 0.06), 0 4px 12px rgba(16, 24, 40, 0.08);
}
h1 {
  margin: 0 0 12px;
  font-size: 1.35rem;
  line-height: 1.3;
}
p {
  margin: 0 0 24px;
  color: #4a5164;
}
p:last-child { margin-bottom: 0; }
button {
  appearance: none;
  border: 0;
  border-radius: 8px;
  background: #2f6fed;
  color: #ffffff;
  font: inherit;
  font-weight: 600;
  padding: 12px 20px;
  cursor: pointer;
}
button:hover { background: #2559c4; }
button:focus-visible { outline: 3px solid rgba(47, 111, 237, 0.4); outline-offset: 2px; }
noscript { display: block; color: #4a5164; }`

// formPageShell wraps page content in the shared document. The title is
// escaped here so no caller can forget to.
func formPageShell(title, bodyClass, content string) []byte {
	classAttr := ""
	if bodyClass != "" {
		classAttr = fmt.Sprintf(" class=%q", bodyClass)
	}

	return []byte(fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>%s</title>
<style>
%s
</style>
</head>
<body%s>
%s
</body>
</html>
`, html.EscapeString(title), formPageCSS, classAttr, content))
}

// formConfirmPage is what the link in a confirmation email opens. The token
// travels on as a hidden input so that confirming is a POST: fetching this
// page must never be enough to confirm an address.
func formConfirmPage(actionPath, token string) []byte {
	content := fmt.Sprintf(`<main class="card">
<h1>Confirm your email address</h1>
<p>One step left. Press the button below to confirm this address and complete your submission.</p>
<form method="POST" action="%s">
<input type="hidden" name="token" value="%s">
<button type="submit">Confirm email</button>
</form>
</main>`, html.EscapeString(actionPath), html.EscapeString(token))

	return formPageShell("Confirm your email address", "", content)
}

// formConfirmedPage is the success outcome of pressing that button.
func formConfirmedPage() []byte {
	content := `<main class="card">
<h1>Email confirmed</h1>
<p>Thank you. Your email address has been confirmed and your submission is complete — you can close this page.</p>
</main>`

	return formPageShell("Email confirmed", "", content)
}

// formInvalidLinkPage is every other outcome. It is deliberately vague: an
// unknown token, a spent one and an expired one all end up here, and the page
// says nothing that would let someone tell them apart.
func formInvalidLinkPage() []byte {
	content := `<main class="card">
<h1>This link is no longer valid</h1>
<p>The confirmation link you followed is invalid or has expired. Confirmation links can only be used once. Please submit the form again to receive a new one.</p>
</main>`

	return formPageShell("Link invalid or expired", "", content)
}

// formHostedViewPage is the shareable URL of a form: a bare shell that loads
// the renderer for one key. The script fills in the name and the fields, so
// nothing about the form is duplicated here — and nothing is revealed when the
// key does not resolve.
func formHostedViewPage(scriptSrc, key string) []byte {
	content := fmt.Sprintf(`<main>
<script src="%s" data-form-key="%s" async></script>
<noscript>This form needs JavaScript. Please enable it and reload the page.</noscript>
</main>`, html.EscapeString(scriptSrc), html.EscapeString(key))

	return formPageShell("Form", "form-page", content)
}
