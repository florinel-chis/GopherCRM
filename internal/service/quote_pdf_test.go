package service

// Quote PDF output at the service (#384): off without a renderer or a base
// URL; a render that mints a credential, hands the renderer the render page's
// URL and requires the page to have loaded the quote; one render at a time
// within the bound; a credential that opens exactly one quote for the life of
// its render; and the time budget of the two calls that can wait for one.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quotePDFTestBytes is what the fake renderers return.
var quotePDFTestBytes = []byte("%PDF-1.7\nfake quote document\n%%EOF\n")

// fakeQuotePDFRenderer stands in for the browser. Like the real page it
// redeems the credential: it takes the fragment of the URL it is given and
// calls GetRenderView with it, unless redeem is off. during runs after that,
// while the render is still in flight.
type fakeQuotePDFRenderer struct {
	svc    func() QuoteService
	pdf    []byte
	err    error
	redeem bool
	// block, when set, holds Render until it is closed, whatever the context.
	block  chan struct{}
	during func(token string, view *models.QuoteRenderView, viewErr error)

	calls atomic.Int32
	mu    sync.Mutex
	urls  []string
}

func (r *fakeQuotePDFRenderer) Render(_ context.Context, pageURL string) ([]byte, error) {
	r.calls.Add(1)
	r.mu.Lock()
	r.urls = append(r.urls, pageURL)
	r.mu.Unlock()

	token := quoteRenderTokenFromURL(pageURL)
	var view *models.QuoteRenderView
	var viewErr error
	if r.redeem {
		view, viewErr = r.svc().GetRenderView(token)
	}
	if r.during != nil {
		r.during(token, view, viewErr)
	}
	if r.block != nil {
		<-r.block
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.pdf, nil
}

func (r *fakeQuotePDFRenderer) pageURLs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

// quoteRenderTokenFromURL is the fragment of a render page URL.
func quoteRenderTokenFromURL(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	return u.Fragment
}

// quotePDFNameSource answers general.company_name and nothing else.
type quotePDFNameSource struct {
	name string
	err  error
}

func (s *quotePDFNameSource) GetString(key string) (string, error) {
	if key != ConfigGeneralCompanyName {
		return "", fmt.Errorf("configuration %q not found: %w", key, apperrors.ErrNotFound)
	}
	return s.name, s.err
}

// quoteAttachmentMailer is the test mailer plus mailer.AttachmentSender.
type quoteAttachmentMailer struct {
	*quoteTestMailer
	mu          sync.Mutex
	attachments []quoteTestAttachment
}

type quoteTestAttachment struct {
	To, Subject, Body string
	Attachment        mailer.Attachment
}

func (m *quoteAttachmentMailer) SendWithAttachment(to, subject, body string, a mailer.Attachment) error {
	if err := m.quoteTestMailer.Send(to, subject, body); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachments = append(m.attachments, quoteTestAttachment{To: to, Subject: subject, Body: body, Attachment: a})
	return nil
}

func (m *quoteAttachmentMailer) attached() []quoteTestAttachment {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]quoteTestAttachment(nil), m.attachments...)
}

func (m *quoteAttachmentMailer) resetAll() {
	m.quoteTestMailer.reset(nil, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachments = nil
}

// newQuotePDFEnv is the response-link env with PDF output on over the given
// fake renderer, a mailer that can attach, and an injectable bound.
func newQuotePDFEnv(t *testing.T, renderer *fakeQuotePDFRenderer, bound time.Duration) (*quoteTestEnv, *quoteAttachmentMailer) {
	t.Helper()
	env := newQuoteResponseEnv(t, openQuoteTestSQLite(t))
	attaching := &quoteAttachmentMailer{quoteTestMailer: env.mailer}
	env.svc.mailer = attaching
	renderer.svc = func() QuoteService { return env.svc }
	require.Same(t, env.svc, WithQuotePDF(env.svc, renderer))
	require.True(t, env.svc.PDFAvailable())
	env.svc.pdf.timeout = bound
	return env, attaching
}

func TestQuotePDFOff(t *testing.T) {
	t.Run("a service without a renderer", func(t *testing.T) {
		env := newQuoteResponseEnv(t, openQuoteTestSQLite(t))
		attaching := &quoteAttachmentMailer{quoteTestMailer: env.mailer}
		env.svc.mailer = attaching
		// A nil renderer, as main passes none when the binary is missing.
		require.Same(t, env.svc, WithQuotePDF(env.svc, nil))
		require.False(t, env.svc.PDFAvailable())

		quote := env.sentQuoteWithLines(t)
		pdf, err := env.svc.RenderPDF(quote.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFUnavailable)
		assert.Nil(t, pdf)

		_, err = env.svc.GetRenderView("any-token")
		require.ErrorIs(t, err, apperrors.ErrQuoteRenderRejected)

		attaching.resetAll()
		emailed, err := env.svc.Email(quote.ID)
		require.NoError(t, err)
		require.NotNil(t, emailed.LastEmailedAt, "last_emailed_at is stamped")
		assert.NotNil(t, env.quote(quote.ID).LastEmailedAt)
		assert.Len(t, env.mailer.messages(), 1, "exactly one mail through Mailer.Send")
		assert.Empty(t, attaching.attached(), "and no attachment call")
	})

	t.Run("a renderer without an APP_BASE_URL", func(t *testing.T) {
		env := newQuoteTestEnv(t, openQuoteTestSQLite(t)) // NewQuoteService: no base URL
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true, svc: func() QuoteService { return env.svc }}
		WithQuotePDF(env.svc, renderer)
		require.False(t, env.svc.PDFAvailable(), "nothing to open without a base URL")

		quote := env.sentQuoteWithLines(t)
		_, err := env.svc.RenderPDF(quote.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFUnavailable)
		_, err = env.svc.Email(quote.ID)
		require.NoError(t, err)
		assert.Len(t, env.mailer.messages(), 1)
		assert.Zero(t, renderer.calls.Load(), "the renderer is never called")
	})

	t.Run("WithQuotePDF leaves another implementation alone", func(t *testing.T) {
		var other QuoteService
		assert.Nil(t, WithQuotePDF(other, &fakeQuotePDFRenderer{}))
	})
}

func TestQuotePDFRender(t *testing.T) {
	t.Run("the renderer's bytes come back and the page URL carries the credential in its fragment", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true}
		env, _ := newQuotePDFEnv(t, renderer, 5*time.Second)
		quote := env.sentQuoteWithLines(t)

		pdf, err := env.svc.RenderPDF(quote.ID)
		require.NoError(t, err)
		assert.Equal(t, quotePDFTestBytes, pdf)

		urls := renderer.pageURLs()
		require.Len(t, urls, 1)
		// The base URL was configured with a trailing slash: it is not doubled.
		require.True(t, strings.HasPrefix(urls[0], quoteResponseTestBaseURL+"/quote-render#"), urls[0])
		u, err := url.Parse(urls[0])
		require.NoError(t, err)
		assert.Empty(t, u.RawQuery, "the credential is in the fragment, never in the query")
		assert.GreaterOrEqual(t, len(u.Fragment), 43, "32 random bytes, base64url")

		// A second render mints another credential.
		_, err = env.svc.RenderPDF(quote.ID)
		require.NoError(t, err)
		urls = renderer.pageURLs()
		require.Len(t, urls, 2)
		assert.NotEqual(t, urls[0], urls[1])
		assert.Empty(t, env.svc.pdf.credentials, "no credential outlives its render")
	})

	t.Run("an unknown quote is not found and nothing is rendered", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true}
		env, _ := newQuotePDFEnv(t, renderer, 5*time.Second)
		_, err := env.svc.RenderPDF(999999)
		require.ErrorIs(t, err, apperrors.ErrQuoteNotFound)
		assert.Zero(t, renderer.calls.Load())
	})

	t.Run("a renderer error is a failed render whose text carries nothing of the renderer's", func(t *testing.T) {
		renderer := &fakeQuotePDFRenderer{redeem: true}
		env, _ := newQuotePDFEnv(t, renderer, 5*time.Second)
		quote := env.sentQuoteWithLines(t)
		output, _ := capturePrivacyLogs(t)
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			renderer.err = fmt.Errorf("chrome said: navigation to %s/quote-render#%s failed", quoteResponseTestBaseURL, token)
		}

		pdf, err := env.svc.RenderPDF(quote.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		assert.Nil(t, pdf)
		urls := renderer.pageURLs()
		require.Len(t, urls, 1)
		token := quoteRenderTokenFromURL(urls[0])
		require.NotEmpty(t, token)
		for _, needle := range []string{token, "quote-render", "chrome said"} {
			assert.NotContains(t, err.Error(), needle)
			assert.NotContains(t, output.String(), needle)
		}
		assert.Empty(t, env.svc.pdf.credentials)
	})

	t.Run("a page that never loaded the quote is a failed render", func(t *testing.T) {
		// The browser exits cleanly with a PDF of an error page: APP_BASE_URL
		// unreachable, or CORS refusing the page's call.
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: false}
		env, _ := newQuotePDFEnv(t, renderer, 5*time.Second)
		quote := env.sentQuoteWithLines(t)
		pdf, err := env.svc.RenderPDF(quote.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		assert.Nil(t, pdf)
		assert.EqualValues(t, 1, renderer.calls.Load())
	})

	t.Run("a renderer that blocks past the bound is a failed render at the bound", func(t *testing.T) {
		const bound = 300 * time.Millisecond
		renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true, block: make(chan struct{})}
		env, _ := newQuotePDFEnv(t, renderer, bound)
		quote := env.sentQuoteWithLines(t)

		started := time.Now()
		_, err := env.svc.RenderPDF(quote.ID)
		elapsed := time.Since(started)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		assert.GreaterOrEqual(t, elapsed, bound)
		assert.Less(t, elapsed, bound+2*time.Second)
		close(renderer.block)
	})

	t.Run("queued render fails within the bound", func(t *testing.T) {
		const bound = 400 * time.Millisecond
		first := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true, block: make(chan struct{})}
		env, _ := newQuotePDFEnv(t, first, bound)
		quote := env.sentQuoteWithLines(t)

		// The first render takes the slot and blocks in its renderer.
		entered := make(chan struct{})
		first.during = func(string, *models.QuoteRenderView, error) { close(entered) }
		firstDone := make(chan error, 1)
		go func() {
			_, err := env.svc.RenderPDF(quote.ID)
			firstDone <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the first render never reached its renderer")
		}

		// The second render gets its own renderer, so a call would show.
		second := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true, svc: first.svc}
		env.svc.pdf.renderer = second
		started := time.Now()
		pdf, err := env.svc.RenderPDF(quote.ID)
		elapsed := time.Since(started)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		assert.Nil(t, pdf)
		assert.Less(t, elapsed, bound+2*time.Second, "the wait for a turn counts against the bound")
		assert.Zero(t, second.calls.Load(), "a render that did not get its turn never starts a browser")

		require.ErrorIs(t, <-firstDone, apperrors.ErrQuotePDFRenderFailed, "the first one ran out of time itself")
		// Once the first renderer has really returned the slot is free again.
		close(first.block)
		require.Eventually(t, func() bool {
			pdf, err := env.svc.RenderPDF(quote.ID)
			return err == nil && len(pdf) > 0
		}, 5*time.Second, 20*time.Millisecond)
		assert.EqualValues(t, 1, second.calls.Load())
	})
}

func TestQuoteRenderCredential(t *testing.T) {
	renderer := &fakeQuotePDFRenderer{pdf: quotePDFTestBytes, redeem: true}
	env, _ := newQuotePDFEnv(t, renderer, 5*time.Second)
	names := &quotePDFNameSource{name: "  Analytical CRM  "}
	env.svc.currency = names
	quoteA := env.sentQuoteWithLines(t)
	quoteB := env.newQuote(models.QuoteStatusSent, 1)
	require.NotEqual(t, quoteA.ID, quoteB.ID)

	t.Run("valid while its render runs: the quote, its lines and the product name", func(t *testing.T) {
		var seen *models.QuoteRenderView
		var seenErr error
		var token string
		renderer.during = func(tok string, view *models.QuoteRenderView, err error) {
			token, seen, seenErr = tok, view, err
		}
		_, err := env.svc.RenderPDF(quoteA.ID)
		require.NoError(t, err)
		require.NoError(t, seenErr)
		require.NotNil(t, seen)
		assert.Equal(t, quoteA.ID, seen.Quote.ID)
		assert.Equal(t, fmt.Sprintf("Q-%06d", quoteA.ID), seen.Quote.Number())
		assert.Len(t, seen.Quote.Lines, 2)
		assert.Equal(t, "Analytical CRM", seen.ProductName, "general.company_name, trimmed")

		t.Run("rejected once the render has finished", func(t *testing.T) {
			view, err := env.svc.GetRenderView(token)
			require.ErrorIs(t, err, apperrors.ErrQuoteRenderRejected)
			assert.Nil(t, view)
		})
	})

	t.Run("a blank or unreadable product name is empty", func(t *testing.T) {
		for _, source := range []*quotePDFNameSource{{name: "   "}, {err: errors.New("configuration store down")}} {
			env.svc.currency = source
			var seen *models.QuoteRenderView
			renderer.during = func(_ string, view *models.QuoteRenderView, _ error) { seen = view }
			_, err := env.svc.RenderPDF(quoteA.ID)
			require.NoError(t, err)
			require.NotNil(t, seen)
			assert.Empty(t, seen.ProductName)
		}
		env.svc.currency = names
	})

	t.Run("rejected after its lifetime, with the render still running", func(t *testing.T) {
		base := time.Now()
		var offset atomic.Int64
		env.svc.now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
		defer func() { env.svc.now = time.Now }()
		renderer.redeem = false
		defer func() { renderer.redeem = true }()

		// One instant before the expiry the one redemption still works and
		// the render gives the PDF...
		var early error
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			offset.Store(int64(env.svc.pdf.timeout - time.Millisecond))
			_, early = env.svc.GetRenderView(token)
		}
		_, err := env.svc.RenderPDF(quoteA.ID)
		require.NoError(t, err)
		require.NoError(t, early)

		// ...and at the expiry a fresh credential is refused and its render
		// fails.
		offset.Store(0)
		var late error
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			offset.Store(int64(env.svc.pdf.timeout))
			_, late = env.svc.GetRenderView(token)
		}
		_, err = env.svc.RenderPDF(quoteA.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		require.ErrorIs(t, late, apperrors.ErrQuoteRenderRejected)
	})

	t.Run("quote A's credential never yields quote B", func(t *testing.T) {
		var viewA *models.QuoteRenderView
		renderer.during = func(_ string, view *models.QuoteRenderView, _ error) { viewA = view }
		_, err := env.svc.RenderPDF(quoteA.ID)
		require.NoError(t, err)
		require.NotNil(t, viewA)
		assert.Equal(t, quoteA.ID, viewA.Quote.ID)
		assert.NotEqual(t, quoteB.ID, viewA.Quote.ID)
	})

	t.Run("a second redemption is refused and the render fails", func(t *testing.T) {
		var first, second error
		var again *models.QuoteRenderView
		renderer.during = func(token string, _ *models.QuoteRenderView, viewErr error) {
			first = viewErr
			again, second = env.svc.GetRenderView(token)
		}
		pdf, err := env.svc.RenderPDF(quoteA.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		assert.Nil(t, pdf)
		require.NoError(t, first)
		require.ErrorIs(t, second, apperrors.ErrQuoteRenderRejected)
		assert.Nil(t, again)
	})

	t.Run("two concurrent redemptions: exactly one succeeds, the render fails", func(t *testing.T) {
		renderer.redeem = false
		defer func() { renderer.redeem = true }()
		var views [2]*models.QuoteRenderView
		var errs [2]error
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range views {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					views[i], errs[i] = env.svc.GetRenderView(token)
				}(i)
			}
			close(start)
			wg.Wait()
		}
		_, err := env.svc.RenderPDF(quoteA.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)

		succeeded := 0
		for i := range views {
			if errs[i] == nil {
				succeeded++
				require.NotNil(t, views[i])
				assert.Equal(t, quoteA.ID, views[i].Quote.ID)
				continue
			}
			require.ErrorIs(t, errs[i], apperrors.ErrQuoteRenderRejected)
			assert.Nil(t, views[i])
		}
		assert.Equal(t, 1, succeeded)
	})

	t.Run("an empty, unknown or foreign token is the one rejection", func(t *testing.T) {
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			for _, bad := range []string{"", "unknown-token", token + "x", strings.ToUpper(token), fmt.Sprint(quoteB.ID)} {
				if bad == token {
					continue
				}
				view, err := env.svc.GetRenderView(bad)
				require.ErrorIs(t, err, apperrors.ErrQuoteRenderRejected, "%q", bad)
				assert.Nil(t, view)
			}
		}
		_, err := env.svc.RenderPDF(quoteA.ID)
		require.NoError(t, err)
	})

	t.Run("a quote deleted during the render is the rejection and fails the render", func(t *testing.T) {
		doomed := env.newQuote(models.QuoteStatusSent, 1)
		renderer.redeem = false
		defer func() { renderer.redeem = true }()
		var viewErr error
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			require.NoError(t, env.db.Delete(&models.Quote{}, doomed.ID).Error)
			_, viewErr = env.svc.GetRenderView(token)
		}
		_, err := env.svc.RenderPDF(doomed.ID)
		require.ErrorIs(t, err, apperrors.ErrQuotePDFRenderFailed)
		require.ErrorIs(t, viewErr, apperrors.ErrQuoteRenderRejected)
	})

	t.Run("only a hash of the credential is kept", func(t *testing.T) {
		renderer.during = func(token string, _ *models.QuoteRenderView, _ error) {
			env.svc.pdf.mu.Lock()
			defer env.svc.pdf.mu.Unlock()
			require.Len(t, env.svc.pdf.credentials, 1)
			for key, credential := range env.svc.pdf.credentials {
				assert.NotEqual(t, token, key)
				assert.Equal(t, hashOpaqueToken(token, quoteResponseTestSecret), key)
				assert.Equal(t, quoteA.ID, credential.quoteID)
			}
		}
		_, err := env.svc.RenderPDF(quoteA.ID)
		require.NoError(t, err)
	})
}

// The contract the SPA's request timeout rests on: one render is bounded at
// 15 s, so the email call, render plus SMTP delivery, stays within 45 s.
func TestQuotePDFTimeoutBudget(t *testing.T) {
	assert.Equal(t, 15*time.Second, QuotePDFRenderTimeout)
	assert.LessOrEqual(t, QuotePDFRenderTimeout+mailer.DefaultSendTimeout, 45*time.Second)

	// A service gets exactly that bound.
	svc := WithQuotePDF(&quoteService{appBaseURL: "https://crm.example.test"}, &fakeQuotePDFRenderer{}).(*quoteService)
	assert.Equal(t, QuotePDFRenderTimeout, svc.pdf.timeout)
}
