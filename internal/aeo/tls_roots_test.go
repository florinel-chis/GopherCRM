package aeo

import (
	"context"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAEOTestTLSRootsStayOutOfProduction(t *testing.T) {
	if os.Getenv("AEO_TLS_ISOLATION_CHILD") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, os.Getenv("AEO_TLS_ISOLATION_URL"), nil)
		require.NoError(t, err)
		response, err := http.DefaultClient.Do(request)
		trusted, parseErr := strconv.ParseBool(os.Getenv("AEO_TLS_ISOLATION_TRUSTED"))
		require.NoError(t, parseErr)
		if trusted {
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.NotEmpty(t, response.TLS.VerifiedChains)
		} else {
			require.ErrorContains(t, err, "certificate signed by unknown authority")
		}
		return
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	certPath := filepath.Join(t.TempDir(), "test-ca.pem")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
	}), 0600))
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, enabled := range []string{"0", "1"} {
		t.Run("opt_in_"+enabled, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "-test.run=^TestAEOTestTLSRootsStayOutOfProduction$")
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(name, "AEO_TLS_ISOLATION_") || name == "GOPHERCRM_E2E_AEO_TLS" ||
					name == "E2E_AEO_CERT" || name == "GODEBUG" || name == "SSL_CERT_FILE" || name == "SSL_CERT_DIR" {
					continue
				}
				command.Env = append(command.Env, entry)
			}
			command.Env = append(command.Env,
				"AEO_TLS_ISOLATION_CHILD=1",
				"AEO_TLS_ISOLATION_URL="+server.URL,
				"AEO_TLS_ISOLATION_TRUSTED="+strconv.FormatBool(e2eTLSBuild && enabled == "1"),
				"GOPHERCRM_E2E_AEO_TLS="+enabled,
				"E2E_AEO_CERT="+certPath,
				"GODEBUG=x509usefallbackroots=1",
			)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
}
