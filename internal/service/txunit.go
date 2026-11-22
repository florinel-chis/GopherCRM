package service

import (
	"context"

	"github.com/florinel-chis/gophercrm/internal/repository"
	"github.com/florinel-chis/gophercrm/internal/utils"
	"github.com/florinel-chis/gophercrm/internal/webhook"
	"gorm.io/gorm"
)

// txUnit is what a write running in one transaction works with: the
// transaction handle, the repositories bound to it, and the webhook and
// automation outlets that write into the same transaction. Everything a
// callback of txDeps.Run writes through it commits or rolls back together.
type txUnit struct {
	// Tx is the active transaction.
	Tx *gorm.DB
	// Customers is the service's customer repository bound to Tx, or nil
	// when the service has none.
	Customers repository.CustomerRepository
	// Webhooks writes webhook events into the outbox of Tx; with no emitter
	// configured it writes nothing.
	Webhooks webhook.TxEmitter
	// Automation writes automation events through Tx, or is nil when the
	// service runs without automation.
	Automation repository.AutomationRepository
}

// txDeps holds what a service needs to run its writes as txUnits: the
// transaction manager (required), the webhook emitter (webhook.Nop unless
// set), the optional automation repository and the base repositories bound
// into each unit.
type txDeps struct {
	tm         *utils.TransactionManager
	webhooks   webhook.Emitter
	automation repository.AutomationRepository
	customers  repository.CustomerRepository
}

// txManagerRequired is the panic value of newTxDeps without a manager.
const txManagerRequired = "service: a transaction manager is required (nil *utils.TransactionManager)"

// newTxDeps returns the dependencies of a service whose writes run through
// Run. It panics when tm is nil: a service built without a transaction
// manager is a wiring mistake, not a mode.
func newTxDeps(tm *utils.TransactionManager) txDeps {
	if tm == nil {
		panic(txManagerRequired)
	}
	return txDeps{tm: tm, webhooks: webhook.Nop}
}

// Run opens one transaction on ctx (or a savepoint when ctx already carries
// one), builds the unit bound to it and calls fn with the transaction's
// context. fn's error is returned unchanged and rolls the transaction back.
func (d txDeps) Run(ctx context.Context, fn func(ctx context.Context, u txUnit) error) error {
	return d.tm.WithTransaction(ctx, func(ctx context.Context) error {
		tx, ok := utils.GetTxFromContext(ctx)
		if !ok {
			return utils.ErrNoTransaction
		}
		emitter := d.webhooks
		if emitter == nil {
			emitter = webhook.Nop
		}
		u := txUnit{
			Tx:         tx,
			Webhooks:   emitter.Bind(tx),
			Automation: automationTx(d.automation, tx),
		}
		if d.customers != nil {
			u.Customers = d.customers.WithTx(tx)
		}
		return fn(ctx, u)
	})
}
