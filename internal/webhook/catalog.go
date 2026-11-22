// Package webhook holds the one catalogue of the entities whose writes emit
// outbound webhook events, and the emitter every write path calls to write
// those events into the outbox.
package webhook

import (
	"sort"
	"strconv"
	"strings"
)

// Entity names a record kind that emits webhook events. Its value is the
// prefix of the event type ("lead" in "lead.created").
type Entity string

// The registered entities.
const (
	Lead     Entity = "lead"
	Customer Entity = "customer"
	Ticket   Entity = "ticket"
	Deal     Entity = "deal"
	Task     Entity = "task"
	Company  Entity = "company"
)

// Action is what happened to a record. Its value is the suffix of the event
// type ("created" in "lead.created").
type Action string

// The actions every registered entity emits.
const (
	Created Action = "created"
	Updated Action = "updated"
	Deleted Action = "deleted"
)

// actions is the order in which an entity's event types are listed in the
// validation messages.
var actions = []Action{Created, Updated, Deleted}

// Catalogue is a set of registered entities. Every event type, the event
// type check and the validation messages are derived from it, so an entity
// added to the catalogue needs no other list updated.
type Catalogue struct {
	entities []Entity
	inOrder  []string
	sorted   []string
	known    map[string]struct{}
}

// NewCatalogue registers entities in the given order; a repeated entity is
// registered once.
func NewCatalogue(entities ...Entity) Catalogue {
	c := Catalogue{known: map[string]struct{}{}}
	seen := map[Entity]struct{}{}
	for _, e := range entities {
		if _, dup := seen[e]; dup {
			continue
		}
		seen[e] = struct{}{}
		c.entities = append(c.entities, e)
		for _, a := range actions {
			t := string(e) + "." + string(a)
			c.inOrder = append(c.inOrder, t)
			c.known[t] = struct{}{}
		}
	}
	c.sorted = append([]string(nil), c.inOrder...)
	sort.Strings(c.sorted)
	return c
}

// defaultCatalogue is the only registration of the shipped entities.
var defaultCatalogue = NewCatalogue(Lead, Customer, Ticket, Deal, Task, Company)

// Default returns the catalogue of the shipped entities: lead, customer,
// ticket, deal, task and company, in that registration order.
func Default() Catalogue { return defaultCatalogue }

// Registers reports whether e is registered.
func (c Catalogue) Registers(e Entity) bool {
	for _, known := range c.entities {
		if known == e {
			return true
		}
	}
	return false
}

// EventType returns "<entity>.<action>", or false when e is not registered
// or a is not an action.
func (c Catalogue) EventType(e Entity, a Action) (string, bool) {
	t := string(e) + "." + string(a)
	if !c.Registers(e) {
		return "", false
	}
	if _, ok := c.known[t]; !ok {
		return "", false
	}
	return t, true
}

// AllEventTypes returns every event type in ascending order. The slice is a
// copy.
func (c Catalogue) AllEventTypes() []string {
	return append([]string(nil), c.sorted...)
}

// IsEventType reports whether s is one of the event types, compared byte for
// byte. The test ping's type (models.WebhookEventPing) is never one.
func (c Catalogue) IsEventType(s string) bool {
	_, ok := c.known[s]
	return ok
}

// SubscribeRule is the refusal message of an endpoint's event_types: "must
// list 1 to N of " and the types in registration order, the last joined with
// " and ".
func (c Catalogue) SubscribeRule() string {
	return "must list 1 to " + strconv.Itoa(len(c.inOrder)) + " of " + joinList(c.inOrder, " and ")
}

// FilterRule is the refusal message of the delivery log's event_type filter:
// "must be " and the types in registration order, the last joined with
// " or ".
func (c Catalogue) FilterRule() string {
	return "must be " + joinList(c.inOrder, " or ")
}

// joinList joins items with ", " and puts last before the final item.
func joinList(items []string, last string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + last + items[len(items)-1]
}

// EventType is Default().EventType.
func EventType(e Entity, a Action) (string, bool) { return defaultCatalogue.EventType(e, a) }

// AllEventTypes is Default().AllEventTypes.
func AllEventTypes() []string { return defaultCatalogue.AllEventTypes() }

// IsEventType is Default().IsEventType.
func IsEventType(s string) bool { return defaultCatalogue.IsEventType(s) }

// SubscribeRule is Default().SubscribeRule.
func SubscribeRule() string { return defaultCatalogue.SubscribeRule() }

// FilterRule is Default().FilterRule.
func FilterRule() string { return defaultCatalogue.FilterRule() }
