package example

import (
	"context"
	"testing"
	"time"

	"github.com/florinel-chis/gophercrm/internal/idkit"
	"github.com/stretchr/testify/require"
)

// TestExampleSessionEnder: a session the store issued with a provider session
// id ends through the SessionEnder, together with the account's other
// sessions; another account keeps its own.
func TestExampleSessionEnder(t *testing.T) {
	store := newStore(idkit.Config{IssuerURL: "https://issuer.example.test"}, 0)
	issue := func(account, sid string) {
		_, err := store.SessionIssuer().Issue(context.Background(), idkit.SessionRequest{
			AccountID: account, Method: idkit.SignInMethodOIDC, SignedInAt: time.Now(),
			Issuer: "https://issuer.example.test", ProviderSessionID: sid})
		require.NoError(t, err)
	}
	issue("1", "sid-a")
	issue("1", "")
	issue("2", "sid-b")
	ended, err := store.SessionEnder().EndProviderSession(context.Background(), "https://issuer.example.test", "sid-a")
	require.NoError(t, err)
	require.Equal(t, 1, ended)
	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.st.sessions, 1, "both sessions of account 1 ended")
	for _, account := range store.st.sessions {
		require.Equal(t, "2", account)
	}
}
