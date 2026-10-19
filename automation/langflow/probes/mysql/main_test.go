package main

import "testing"

func TestConfigurationsRequireExplicitDisposableServers(t *testing.T) {
	valid := map[string]string{"TEST_MYSQL_DSN": "user:password@tcp(127.0.0.1:13308)/",
		"DB_HOST": "127.0.0.1", "DB_PORT": "13308", "DB_USER": "runner", "DB_PASSWORD": "private",
		"E2E_DB_NAME": "runner_e2e"}
	get := func(k string) string { return valid[k] }
	for _, field := range []string{"TEST_MYSQL_DSN", "DB_HOST", "DB_PORT", "DB_USER", "E2E_DB_NAME"} {
		saved := valid[field]
		valid[field] = ""
		if _, err := configurations(get); err == nil {
			t.Fatalf("missing %s accepted", field)
		}
		valid[field] = saved
	}
	for _, dsn := range []string{"user:password@tcp(127.0.0.1:13308)/production", "user@unix(/tmp/mysql.sock)/"} {
		valid["TEST_MYSQL_DSN"] = dsn
		if _, err := configurations(get); err == nil {
			t.Fatalf("unsafe DSN accepted")
		}
	}
}

func TestConfigurationsUseExplicitE2ESettingsWithoutSelectingOrCreatingDatabase(t *testing.T) {
	values := map[string]string{"TEST_MYSQL_DSN": "user:password@tcp(127.0.0.1:13308)/?multiStatements=true",
		"DB_HOST": "127.0.0.1", "DB_PORT": "13309", "DB_USER": "runner", "DB_PASSWORD": "private",
		"E2E_DB_NAME": "runner_e2e"}
	configs, err := configurations(func(k string) string { return values[k] })
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range configs {
		if c.DBName != "" || c.MultiStatements || c.Timeout == 0 || c.ReadTimeout == 0 {
			t.Errorf("%s did not restrict the probe configuration", name)
		}
	}
	if c := configs["e2e"]; c.Addr != "127.0.0.1:13309" || c.User != "runner" || c.Passwd != "private" {
		t.Fatal("explicit E2E server settings were not preserved")
	}
}
