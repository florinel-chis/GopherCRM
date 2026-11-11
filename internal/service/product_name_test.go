package service

// Mail product name (#292). The resolver is driven with a stub source; the
// quote and ticket roots run the real services over the real repositories on
// SQLite with a recording mailer; the reset root uses the mock repositories
// and the mock mailer of the session tests.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "github.com/florinel-chis/gophercrm/internal/errors"
	"github.com/florinel-chis/gophercrm/internal/mailer"
	"github.com/florinel-chis/gophercrm/internal/mocks"
	"github.com/florinel-chis/gophercrm/internal/models"
	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// mailNameStub is a MailProductNameSource that records every key it is asked
// for and answers value or err. onCall runs inside GetString.
type mailNameStub struct {
	mu     sync.Mutex
	value  string
	err    error
	keys   []string
	onCall func()
}

func (s *mailNameStub) GetString(key string) (string, error) {
	s.mu.Lock()
	s.keys = append(s.keys, key)
	value, err, hook := s.value, s.err, s.onCall
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return value, err
}

func (s *mailNameStub) set(value string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value, s.err = value, err
}

func (s *mailNameStub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.keys)
}

func (s *mailNameStub) askedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

// mailNameNotFound is the error ConfigurationService.GetString returns for a
// missing row.
func mailNameNotFound() error {
	return fmt.Errorf("configuration %q not found: %w", ConfigGeneralCompanyName, apperrors.ErrNotFound)
}

// mailNameWarnings returns the Warn entries the resolver wrote.
func mailNameWarnings(hook *logtest.Hook) []*logrus.Entry {
	var out []*logrus.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Level == logrus.WarnLevel && entry.Data["config_key"] == ConfigGeneralCompanyName {
			out = append(out, entry)
		}
	}
	return out
}

func TestMailProductNameResolver(t *testing.T) {
	_, _ = capturePrivacyLogs(t)

	for _, tc := range []struct {
		name  string
		value string
		err   error
		want  string
	}{
		{"stored name", "Acme Ltd", nil, "Acme Ltd"},
		{"surrounding whitespace is trimmed", " \t Acme Ltd  ", nil, "Acme Ltd"},
		{"non-ASCII name", "Café Ünited", nil, "Café Ünited"},
		{"empty value", "", nil, DefaultMailProductName},
		{"whitespace-only value", " \t  ", nil, DefaultMailProductName},
		{"missing key", "", mailNameNotFound(), DefaultMailProductName},
		{"other read error", "", errors.New("database is locked"), DefaultMailProductName},
		{"read error with a value", "Acme Ltd", errors.New("configuration is not a string"), DefaultMailProductName},
		{"CR", "Acme\rLtd", nil, DefaultMailProductName},
		{"LF", "Acme\nLtd", nil, DefaultMailProductName},
		{"CRLF", "Acme\r\nBcc: attacker@example.com", nil, DefaultMailProductName},
		{"tab inside the name", "Acme\tLtd", nil, DefaultMailProductName},
		{"other control character", "Acme\x00Ltd", nil, DefaultMailProductName},
		{"C1 control character", "Acme\u0085Ltd", nil, DefaultMailProductName},
		{"invalid UTF-8", "Acme \xff Ltd", nil, DefaultMailProductName},
		{"exactly 100 runes", strings.Repeat("é", MailProductNameMaxRunes), nil, strings.Repeat("é", MailProductNameMaxRunes)},
		{"101 runes", strings.Repeat("é", MailProductNameMaxRunes+1), nil, DefaultMailProductName},
		{"100 runes after trimming", "  " + strings.Repeat("a", MailProductNameMaxRunes) + "  ", nil, strings.Repeat("a", MailProductNameMaxRunes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &mailNameStub{value: tc.value, err: tc.err}
			assert.Equal(t, tc.want, ResolveMailProductName(stub))
			assert.Equal(t, []string{ConfigGeneralCompanyName}, stub.askedKeys(), "exactly one read of general.company_name")
		})
	}

	t.Run("max runes constant", func(t *testing.T) {
		assert.Equal(t, 100, MailProductNameMaxRunes)
		assert.Equal(t, "GopherCRM", DefaultMailProductName)
		assert.Equal(t, "general.company_name", ConfigGeneralCompanyName)
	})

	t.Run("nil source", func(t *testing.T) {
		assert.Equal(t, DefaultMailProductName, ResolveMailProductName(nil))
		var typedNil *configurationService
		assert.Equal(t, DefaultMailProductName, ResolveMailProductName(typedNil))
	})

	t.Run("every call reads the source again", func(t *testing.T) {
		stub := &mailNameStub{value: "First Name"}
		assert.Equal(t, "First Name", ResolveMailProductName(stub))
		stub.set("Second Name", nil)
		assert.Equal(t, "Second Name", ResolveMailProductName(stub), "a changed value is reflected on the next call")
		stub.set("", nil)
		assert.Equal(t, DefaultMailProductName, ResolveMailProductName(stub))
		stub.set("Third Name", nil)
		assert.Equal(t, "Third Name", ResolveMailProductName(stub))
		assert.Equal(t, 4, stub.calls(), "no cache")
	})

	t.Run("the configuration service is a source", func(t *testing.T) {
		repo := &mocks.ConfigurationRepository{}
		row := &models.Configuration{Key: ConfigGeneralCompanyName, Value: "Acme Ltd", Type: models.ConfigTypeString}
		repo.On("GetByKey", ConfigGeneralCompanyName).Return(row, nil).Once()
		repo.On("GetByKey", ConfigGeneralCompanyName).Return(nil, gorm.ErrRecordNotFound).Once()
		svc := NewConfigurationService(repo, nil)
		assert.Equal(t, "Acme Ltd", ResolveMailProductName(svc))
		assert.Equal(t, DefaultMailProductName, ResolveMailProductName(svc), "a missing row falls back")
		repo.AssertExpectations(t)
	})
}

func TestMailProductNameQuoteSubject(t *testing.T) {
	db := openQuoteTestSQLite(t)
	env := newQuoteTestEnv(t, db)

	for _, tc := range []struct {
		name   string
		source func() (DealCurrencySource, *mailNameStub)
		want   string
	}{
		{"stored name", func() (DealCurrencySource, *mailNameStub) {
			s := &mailNameStub{value: "Acme Ltd"}
			return s, s
		}, "Acme Ltd"},
		{"no stored name", func() (DealCurrencySource, *mailNameStub) {
			s := &mailNameStub{err: mailNameNotFound()}
			return s, s
		}, DefaultMailProductName},
		{"blank stored name", func() (DealCurrencySource, *mailNameStub) {
			s := &mailNameStub{value: "  "}
			return s, s
		}, DefaultMailProductName},
		{"failing read", func() (DealCurrencySource, *mailNameStub) {
			s := &mailNameStub{err: errors.New("connection refused")}
			return s, s
		}, DefaultMailProductName},
		{"nil configuration source", func() (DealCurrencySource, *mailNameStub) {
			return nil, nil
		}, DefaultMailProductName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.mailer.reset(nil, nil)
			quote := env.newQuote(models.QuoteStatusSent, 1)
			require.Nil(t, quote.LastEmailedAt)
			source, stub := tc.source()
			env.svc.currency = source
			t.Cleanup(func() { env.svc.currency = nil })

			emailed, err := env.svc.Email(quote.ID)
			require.NoError(t, err, "a configuration read never blocks the send")
			require.NotNil(t, emailed.LastEmailedAt, "last_emailed_at is stamped as before")
			assert.NotNil(t, env.quote(quote.ID).LastEmailedAt)

			messages := env.mailer.messages()
			require.Len(t, messages, 1, "the mail is sent exactly once")
			assert.Equal(t, fmt.Sprintf("Quote %s from %s", models.QuoteNumber(quote.ID), tc.want), messages[0].Subject)
			if stub != nil {
				assert.Equal(t, []string{ConfigGeneralCompanyName}, stub.askedKeys())
			}
		})
	}

	t.Run("Q-000042", func(t *testing.T) {
		assert.Equal(t, "Quote Q-000042 from Acme Ltd", QuoteEmailSubject(42, "Acme Ltd"))
		assert.Equal(t, "Quote Q-000042 from GopherCRM", QuoteEmailSubject(42, DefaultMailProductName))
	})
}

// serviceWithName wires the ticket service with the mail and, when source is
// non-nil, the product name option.
func (e *ticketMailEnv) serviceWithName(m mailer.Mailer, source MailProductNameSource) TicketService {
	opts := []TicketServiceOption{WithTicketCommentMail(e.deliveries, e.tm, m)}
	if source != nil {
		opts = append(opts, WithTicketProductName(source))
	}
	return NewTicketService(repository.NewTicketRepository(e.db), repository.NewCustomerRepository(e.db),
		repository.NewUserRepository(e.db), opts...)
}

func TestMailProductNameTicketSubject(t *testing.T) {
	db := openTicketMailSQLite(t)

	for _, tc := range []struct {
		name     string
		stub     *mailNameStub
		withName bool
		want     string
	}{
		{"stored name", &mailNameStub{value: "Acme Ltd"}, true, "Acme Ltd"},
		{"no stored name", &mailNameStub{err: mailNameNotFound()}, true, DefaultMailProductName},
		{"blank stored name", &mailNameStub{value: ""}, true, DefaultMailProductName},
		{"failing read", &mailNameStub{err: errors.New("connection refused")}, true, DefaultMailProductName},
		{"without WithTicketProductName", nil, false, DefaultMailProductName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTicketMailEnv(t, db)
			recorder := &ticketMailRecorder{}
			var source MailProductNameSource
			if tc.withName {
				source = tc.stub
				// The read happens after the commit with no transaction held:
				// on the one-connection pool a held transaction would make this
				// read wait until its deadline, and an uncommitted pending row
				// would not be visible to it.
				tc.stub.onCall = func() {
					require.Empty(t, recorder.messages(), "the name is read before the send")
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					var pending int64
					require.NoError(t, e.db.WithContext(ctx).Model(&models.TicketMailDelivery{}).
						Where("ticket_id = ? AND status = ?", e.ticket.ID, models.TicketMailStatusPending).
						Count(&pending).Error, "no transaction holds the connection")
					assert.Equal(t, int64(1), pending, "the comment and its pending delivery are committed")
				}
			}
			svc := e.serviceWithName(recorder, source)

			comment := e.comment(e.support, "A reply")
			require.NoError(t, svc.CreateCommentWithCustomerEmail(context.Background(), comment))

			messages := recorder.messages()
			require.Len(t, messages, 1, "Mailer.Send is called exactly once")
			assert.Equal(t, fmt.Sprintf("Ticket %s from %s", models.TicketNumber(e.ticket.ID), tc.want), messages[0].Subject)
			require.NotNil(t, comment.Delivery)
			assert.Equal(t, models.TicketMailStatusSent, comment.Delivery.Status)
			assert.Equal(t, models.TicketMailStatusSent, e.storedDelivery(t, comment.ID).Status, "the delivery row is stamped sent")
			if tc.withName {
				assert.Equal(t, []string{ConfigGeneralCompanyName}, tc.stub.askedKeys())
			}
		})
	}

	t.Run("skipped deliveries make no read", func(t *testing.T) {
		for _, skip := range []struct {
			name    string
			prepare func(t *testing.T, e *ticketMailEnv)
			mailer  bool
			code    string
		}{
			{"no_mailer", func(*testing.T, *ticketMailEnv) {}, false, models.TicketMailErrorNoMailer},
			{"customer_unavailable", func(t *testing.T, e *ticketMailEnv) {
				require.NoError(t, e.db.Delete(&models.Customer{}, e.customer.ID).Error)
			}, true, models.TicketMailErrorCustomerUnavailable},
			{"no_recipient", func(t *testing.T, e *ticketMailEnv) {
				require.NoError(t, e.db.Model(&models.Customer{}).Where("id = ?", e.customer.ID).Update("email", "   ").Error)
			}, true, models.TicketMailErrorNoRecipient},
		} {
			t.Run(skip.name, func(t *testing.T) {
				e := newTicketMailEnv(t, db)
				skip.prepare(t, e)
				stub := &mailNameStub{value: "Acme Ltd"}
				recorder := &ticketMailRecorder{}
				var m mailer.Mailer
				if skip.mailer {
					m = recorder
				}
				svc := e.serviceWithName(m, stub)

				comment := e.comment(e.support, "A reply")
				require.NoError(t, svc.CreateCommentWithCustomerEmail(context.Background(), comment))
				require.NotNil(t, comment.Delivery)
				assert.Equal(t, models.TicketMailStatusSkipped, comment.Delivery.Status)
				assert.Equal(t, skip.code, comment.Delivery.ErrorCode)
				assert.Empty(t, recorder.messages())
				assert.Equal(t, 0, stub.calls(), "a skipped delivery makes no GetString call")
			})
		}
	})
}

// newMailNameAuthService is the session test service with the product name
// option when source is non-nil.
func newMailNameAuthService(t *testing.T, source MailProductNameSource) *sessionTestDeps {
	t.Helper()
	d := &sessionTestDeps{
		userRepo:    &mocks.UserRepository{},
		apiKeyRepo:  &mocks.APIKeyRepository{},
		refreshRepo: &mocks.RefreshTokenRepository{},
		resetRepo:   &mocks.PasswordResetTokenRepository{},
		txManager:   sessionTestTransactionManager(t),
		mailer:      &mockMailer{},
	}
	var opts []AuthServiceOption
	if source != nil {
		opts = append(opts, WithAuthProductName(source))
	}
	d.svc = NewAuthServiceWithSessions(
		d.userRepo, d.apiKeyRepo, d.refreshRepo, d.resetRepo, d.txManager, d.mailer,
		sessionTestJWTConfig, "http://localhost:5173", "", opts...)
	return d
}

func TestMailProductNamePasswordReset(t *testing.T) {
	_, _ = capturePrivacyLogs(t)

	for _, tc := range []struct {
		name     string
		stub     *mailNameStub
		withName bool
		want     string
	}{
		{"stored name", &mailNameStub{value: "Acme Ltd"}, true, "Acme Ltd"},
		{"no stored name", &mailNameStub{err: mailNameNotFound()}, true, DefaultMailProductName},
		{"blank stored name", &mailNameStub{value: " "}, true, DefaultMailProductName},
		{"failing read", &mailNameStub{err: errors.New("connection refused")}, true, DefaultMailProductName},
		{"without WithAuthProductName", nil, false, DefaultMailProductName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var source MailProductNameSource
			if tc.withName {
				source = tc.stub
			}
			d := newMailNameAuthService(t, source)
			user := sessionActiveUser(1, "CurrentPass1!")
			d.userRepo.On("GetByEmail", user.Email).Return(user, nil)
			d.resetRepo.On("InvalidateAllForUser", uint(1)).Return(nil)
			d.resetRepo.On("Create", mock.Anything).Return(nil)
			d.mailer.On("SendPasswordReset", user.Email, mock.MatchedBy(func(u string) bool {
				return strings.HasPrefix(u, "http://localhost:5173/reset-password?token=")
			}), tc.want).Return(nil)

			require.NoError(t, d.svc.RequestPasswordReset(user.Email))
			d.mailer.AssertNumberOfCalls(t, "SendPasswordReset", 1)
			d.mailer.AssertExpectations(t)
			if tc.withName {
				assert.Equal(t, []string{ConfigGeneralCompanyName}, tc.stub.askedKeys())
			}
		})
	}

	t.Run("unknown email reads nothing and sends nothing", func(t *testing.T) {
		stub := &mailNameStub{value: "Acme Ltd"}
		d := newMailNameAuthService(t, stub)
		d.userRepo.On("GetByEmail", "ghost@example.com").Return(nil, gorm.ErrRecordNotFound)

		require.NoError(t, d.svc.RequestPasswordReset("ghost@example.com"))
		assert.Equal(t, 0, stub.calls())
		d.mailer.AssertNotCalled(t, "SendPasswordReset", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("inactive account reads nothing and sends nothing", func(t *testing.T) {
		stub := &mailNameStub{value: "Acme Ltd"}
		d := newMailNameAuthService(t, stub)
		user := sessionActiveUser(1, "CurrentPass1!")
		user.IsActive = false
		d.userRepo.On("GetByEmail", user.Email).Return(user, nil)

		require.NoError(t, d.svc.RequestPasswordReset(user.Email))
		assert.Equal(t, 0, stub.calls())
		d.mailer.AssertNotCalled(t, "SendPasswordReset", mock.Anything, mock.Anything, mock.Anything)
	})
}

func TestMailProductNameLogsOmitValue(t *testing.T) {
	const (
		accepted = "MarkerAccepted-Kq19 Ltd"
		rejected = "MarkerRejected-Zp44"
	)
	rejectedValues := map[string]string{
		"CR":          rejected + "\rLtd",
		"LF":          rejected + "\nLtd",
		"control":     rejected + "\x07Ltd",
		"too long":    rejected + strings.Repeat("x", MailProductNameMaxRunes),
		"invalid utf": rejected + "\xff",
	}
	assertNoMarker := func(t *testing.T, output string) {
		t.Helper()
		assert.NotContains(t, output, "MarkerAccepted-Kq19")
		assert.NotContains(t, output, "MarkerRejected-Zp44")
	}

	t.Run("resolver", func(t *testing.T) {
		output, hook := capturePrivacyLogs(t)

		hook.Reset()
		assert.Equal(t, accepted, ResolveMailProductName(&mailNameStub{value: accepted}))
		assert.Empty(t, mailNameWarnings(hook), "an accepted name logs no warning")

		for name, value := range rejectedValues {
			hook.Reset()
			assert.Equal(t, DefaultMailProductName, ResolveMailProductName(&mailNameStub{value: value}), name)
			warnings := mailNameWarnings(hook)
			require.Len(t, warnings, 1, "exactly one Warn line for %s", name)
			assert.Equal(t, "invalid_value", warnings[0].Data["reason"])
			assert.Equal(t, ConfigGeneralCompanyName, warnings[0].Data["config_key"])
			_, hasErr := warnings[0].Data[logrus.ErrorKey]
			assert.False(t, hasErr, "an invalid value carries no error")
		}

		for name, err := range map[string]error{"missing": mailNameNotFound(), "failing": errors.New("connection refused")} {
			hook.Reset()
			assert.Equal(t, DefaultMailProductName, ResolveMailProductName(&mailNameStub{value: accepted, err: err}), name)
			warnings := mailNameWarnings(hook)
			require.Len(t, warnings, 1, "exactly one Warn line for %s", name)
			assert.Equal(t, "lookup_failed", warnings[0].Data["reason"])
			assert.Equal(t, ConfigGeneralCompanyName, warnings[0].Data["config_key"])
			assert.NotNil(t, warnings[0].Data[logrus.ErrorKey], "the read error is logged through utils.LogError")
		}

		hook.Reset()
		assert.Equal(t, DefaultMailProductName, ResolveMailProductName(&mailNameStub{value: "   "}))
		assert.Equal(t, DefaultMailProductName, ResolveMailProductName(nil))
		assert.Empty(t, hook.AllEntries(), "a blank value or a nil source logs nothing")

		assertNoMarker(t, output.String())
	})

	values := map[string]string{"accepted": accepted}
	for name, value := range rejectedValues {
		values["rejected "+name] = value
	}

	t.Run("quote send path", func(t *testing.T) {
		db := openQuoteTestSQLite(t)
		output, hook := capturePrivacyLogs(t)
		env := newQuoteTestEnv(t, db)
		for name, value := range values {
			env.mailer.reset(nil, nil)
			quote := env.newQuote(models.QuoteStatusSent, 1)
			hook.Reset()
			env.svc.currency = &mailNameStub{value: value}
			_, err := env.svc.Email(quote.ID)
			env.svc.currency = nil
			require.NoError(t, err, name)
			require.Len(t, env.mailer.messages(), 1, name)
			if value == accepted {
				assert.Empty(t, mailNameWarnings(hook))
			} else {
				assert.Len(t, mailNameWarnings(hook), 1, name)
			}
		}
		assertNoMarker(t, output.String())
	})

	t.Run("ticket send path", func(t *testing.T) {
		db := openTicketMailSQLite(t)
		for name, value := range values {
			e := newTicketMailEnv(t, db)
			output, hook := capturePrivacyLogs(t)
			recorder := &ticketMailRecorder{}
			svc := e.serviceWithName(recorder, &mailNameStub{value: value})
			comment := e.comment(e.support, "A reply")
			require.NoError(t, svc.CreateCommentWithCustomerEmail(context.Background(), comment), name)
			require.Len(t, recorder.messages(), 1, name)
			if value == accepted {
				assert.Empty(t, mailNameWarnings(hook))
			} else {
				assert.Len(t, mailNameWarnings(hook), 1, name)
			}
			assertNoMarker(t, output.String())
		}
	})

	t.Run("password reset send path", func(t *testing.T) {
		for name, value := range values {
			output, hook := capturePrivacyLogs(t)
			d := newMailNameAuthService(t, &mailNameStub{value: value})
			user := sessionActiveUser(1, "CurrentPass1!")
			d.userRepo.On("GetByEmail", user.Email).Return(user, nil)
			d.resetRepo.On("InvalidateAllForUser", uint(1)).Return(nil)
			d.resetRepo.On("Create", mock.Anything).Return(nil)
			d.mailer.On("SendPasswordReset", user.Email, mock.Anything, mock.Anything).Return(nil)
			require.NoError(t, d.svc.RequestPasswordReset(user.Email), name)
			d.mailer.AssertNumberOfCalls(t, "SendPasswordReset", 1)
			if value == accepted {
				assert.Empty(t, mailNameWarnings(hook))
			} else {
				assert.Len(t, mailNameWarnings(hook), 1, name)
			}
			assertNoMarker(t, output.String())
		}
	})
}
