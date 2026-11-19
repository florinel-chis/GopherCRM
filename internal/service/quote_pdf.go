package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/utils"
)

// Quote PDF output (#384). A renderer prints the public render page of the
// SPA, <APP_BASE_URL>/quote-render, which shows the same document as the print
// page and loads the quote through POST /quotes/public/render with a render
// credential: 32 random bytes minted for one render, kept only as a hash in
// this process's memory together with the quote id and an expiry, and deleted
// when the render returns. The credential travels in the URL fragment, so no
// server receives it in a URL; it opens one quote, read-only, for at most the
// render bound.
//
// Neither the page URL, the credential nor the renderer's own error text
// reaches a log line or an error: the logs carry the quote id and a reason.

// QuotePDFRenderTimeout bounds one render, the wait for its turn included.
// It is the only place the bound is defined. POST /quotes/:id/email therefore
// takes at most QuotePDFRenderTimeout + mailer.DefaultSendTimeout (45 s) and
// GET /quotes/:id/pdf at most QuotePDFRenderTimeout; the SPA gives both calls
// 55 s, below a reverse proxy's usual 60 s read timeout.
const QuotePDFRenderTimeout = 15 * time.Second

// quoteRenderPath is the SPA route the renderer opens.
const quoteRenderPath = "/quote-render"

// QuotePDFRenderer prints the page at pageURL to PDF and returns the file's
// bytes. It must stop when ctx is done. pageURL carries a credential in its
// fragment: an implementation never logs it and never puts it into an error.
type QuotePDFRenderer interface {
	Render(ctx context.Context, pageURL string) ([]byte, error)
}

// quoteRenderCredential is one live render: the quote it may read, when it
// stops working, how often it was presented while live, and whether the quote
// was loaded with it. It is single use: only the first presentation gets the
// quote, and a render whose credential was presented more than once fails.
type quoteRenderCredential struct {
	quoteID     uint
	expiresAt   time.Time
	redemptions int
	loaded      bool
}

// quotePDF is the PDF state of one quote service: the renderer, the bound,
// the single render slot and the credentials of the renders in flight.
type quotePDF struct {
	renderer QuotePDFRenderer
	timeout  time.Duration
	// slot holds one token while a render runs: at most one browser per
	// process. A render waits for it within its own bound.
	slot chan struct{}

	mu          sync.Mutex
	credentials map[string]*quoteRenderCredential
}

// WithQuotePDF switches PDF output on for a quote service built by
// NewQuoteService or NewQuoteServiceWithResponseLinks and returns it. A nil
// renderer leaves the service as it is (PDF off), and so does a service of
// another implementation. PDF output also needs the APP_BASE_URL the service
// was built with: without one PDFAvailable stays false.
func WithQuotePDF(svc QuoteService, renderer QuotePDFRenderer) QuoteService {
	s, ok := svc.(*quoteService)
	if !ok || renderer == nil {
		return svc
	}
	s.pdf = &quotePDF{
		renderer:    renderer,
		timeout:     QuotePDFRenderTimeout,
		slot:        make(chan struct{}, 1),
		credentials: make(map[string]*quoteRenderCredential),
	}
	return s
}

func (s *quoteService) PDFAvailable() bool {
	return s.pdf != nil && s.appBaseURL != ""
}

func (s *quoteService) RenderPDF(id uint) ([]byte, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("quote_id", id), "QuoteService", "RenderPDF")

	if !s.PDFAvailable() {
		err := fmt.Errorf("quote %d: %w", id, apperrors.ErrQuotePDFUnavailable)
		logger.WithError(utils.LogError(err)).Warn("Quote PDF refused")
		return nil, err
	}
	if _, err := s.GetByID(id); err != nil {
		return nil, err
	}
	pdf, reason, err := s.renderQuotePDF(id)
	if err != nil {
		logger.WithField("reason", reason).Warn("Quote PDF render failed")
		return nil, err
	}
	logger.Info("Quote PDF rendered")
	return pdf, nil
}

// The reasons a render failed, for the log.
const (
	quotePDFReasonQueue         = "no_turn_within_bound"
	quotePDFReasonTimeout       = "timeout"
	quotePDFReasonRenderer      = "renderer_error"
	quotePDFReasonNotRedeemed   = "page_never_loaded_the_quote"
	quotePDFReasonRedeemedTwice = "credential_redeemed_more_than_once"
	quotePDFReasonCredential    = "credential_error"
)

// renderQuotePDF runs one render of a quote that exists. Every failure is
// apperrors.ErrQuotePDFRenderFailed with a reason for the log; the renderer's
// own error is dropped on purpose, because nothing guarantees it is free of
// the page URL.
//
// The bound covers the wait for the slot and the render. The renderer runs in
// its own goroutine and gives the slot back when it has really returned, so a
// renderer that is slow to stop cannot hold the caller past the bound and
// cannot overlap the next render either.
func (s *quoteService) renderQuotePDF(quoteID uint) ([]byte, string, error) {
	failed := fmt.Errorf("quote %d: %w", quoteID, apperrors.ErrQuotePDFRenderFailed)

	ctx, cancel := context.WithTimeout(context.Background(), s.pdf.timeout)
	defer cancel()

	select {
	case s.pdf.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, quotePDFReasonQueue, failed
	}

	token, err := generateOpaqueToken()
	if err != nil {
		<-s.pdf.slot
		return nil, quotePDFReasonCredential, failed
	}
	hash := hashOpaqueToken(token, s.tokenSecret)
	credential := &quoteRenderCredential{quoteID: quoteID, expiresAt: s.now().Add(s.pdf.timeout)}
	s.pdf.mu.Lock()
	s.pdf.credentials[hash] = credential
	s.pdf.mu.Unlock()
	// The credential dies with the render, whatever its outcome.
	defer func() {
		s.pdf.mu.Lock()
		delete(s.pdf.credentials, hash)
		s.pdf.mu.Unlock()
	}()

	type result struct {
		pdf []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-s.pdf.slot }()
		pdf, err := s.pdf.renderer.Render(ctx, s.appBaseURL+quoteRenderPath+"#"+token)
		done <- result{pdf: pdf, err: err}
	}()

	var out result
	select {
	case out = <-done:
	case <-ctx.Done():
		return nil, quotePDFReasonTimeout, failed
	}
	if out.err != nil {
		if errors.Is(out.err, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, quotePDFReasonTimeout, failed
		}
		return nil, quotePDFReasonRenderer, failed
	}
	// A browser that exits cleanly has printed something, but not
	// necessarily the quote: with APP_BASE_URL unreachable, or CORS refusing
	// the page's call, it prints an error page. Only a render whose page
	// loaded the quote counts, and only when the credential was presented
	// exactly once.
	s.pdf.mu.Lock()
	redemptions, loaded := credential.redemptions, credential.loaded
	s.pdf.mu.Unlock()
	if redemptions > 1 {
		return nil, quotePDFReasonRedeemedTwice, failed
	}
	if !loaded {
		return nil, quotePDFReasonNotRedeemed, failed
	}
	return out.pdf, "", nil
}

// rejectQuoteRender wraps the one public refusal of a render credential.
func rejectQuoteRender(reason string) error {
	return fmt.Errorf("%s: %w", reason, apperrors.ErrQuoteRenderRejected)
}

func (s *quoteService) GetRenderView(token string) (*models.QuoteRenderView, error) {
	logger := utils.LogServiceCall(utils.Logger.WithField("entity", "quote"), "QuoteService", "GetRenderView")

	quoteID, hash, err := s.redeemRenderCredential(token)
	if err != nil {
		logger.WithError(utils.LogError(err)).Warn("Quote render credential refused")
		return nil, err
	}
	logger = logger.WithField("quote_id", quoteID)

	// The quote the credential was minted for, and nothing the caller names.
	quote, err := s.quoteRepo.GetByID(quoteID)
	if err != nil {
		if isNotFound(err) {
			err = rejectQuoteRender(fmt.Sprintf("quote %d is gone", quoteID))
			logger.WithError(utils.LogError(err)).Warn("Quote render credential refused")
			return nil, err
		}
		utils.LogServiceResponse(logger, err)
		return nil, err
	}
	s.markRenderCredentialLoaded(hash)
	return &models.QuoteRenderView{Quote: quote, ProductName: s.renderProductName()}, nil
}

// redeemRenderCredential resolves a live credential to its quote id and
// returns the credential's hash. A service without PDF output, an empty token,
// an unknown one and an expired one are all the rejection. The credential is
// single use: the lookup, the expiry check and the count of presentations are
// one critical section, so of two presentations, concurrent or not, only the
// first gets the quote; the later one is the rejection and fails the render.
func (s *quoteService) redeemRenderCredential(token string) (uint, string, error) {
	if !s.PDFAvailable() {
		return 0, "", rejectQuoteRender("PDF output is off")
	}
	if token == "" {
		return 0, "", rejectQuoteRender("no credential")
	}
	hash := hashOpaqueToken(token, s.tokenSecret)
	s.pdf.mu.Lock()
	defer s.pdf.mu.Unlock()
	credential, ok := s.pdf.credentials[hash]
	if !ok {
		return 0, "", rejectQuoteRender("no live credential")
	}
	if !s.now().Before(credential.expiresAt) {
		return 0, "", rejectQuoteRender(fmt.Sprintf("the credential of quote %d has expired", credential.quoteID))
	}
	credential.redemptions++
	if credential.redemptions > 1 {
		return 0, "", rejectQuoteRender(fmt.Sprintf("the credential of quote %d was already redeemed", credential.quoteID))
	}
	return credential.quoteID, hash, nil
}

// markRenderCredentialLoaded records that the quote was read with the
// credential of hash, if that render is still live.
func (s *quoteService) markRenderCredentialLoaded(hash string) {
	s.pdf.mu.Lock()
	defer s.pdf.mu.Unlock()
	if credential, ok := s.pdf.credentials[hash]; ok {
		credential.loaded = true
	}
}

// renderProductName is the stored general.company_name, trimmed; empty when
// there is no configuration source, the read fails or the value is blank. The
// page then heads the document with its built-in name, as the print page does.
func (s *quoteService) renderProductName() string {
	if isNilMailProductNameSource(s.currency) {
		return ""
	}
	value, err := s.currency.GetString(ConfigGeneralCompanyName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// quotePDFFilename is the name of the PDF as a download and as an attachment.
func quotePDFFilename(quoteID uint) string {
	return "Quote-" + models.QuoteNumber(quoteID) + ".pdf"
}
