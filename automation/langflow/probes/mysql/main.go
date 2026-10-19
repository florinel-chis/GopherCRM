// Command mysql performs read-only connectivity checks for the runner doctor.
// It never creates, selects, resets, migrates, or deletes an application database.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

func configurations(get func(string) string) (map[string]*mysql.Config, error) {
	for _, name := range []string{"TEST_MYSQL_DSN", "DB_HOST", "DB_PORT", "DB_USER", "E2E_DB_NAME"} {
		if get(name) == "" {
			return nil, fmt.Errorf("explicit %s is required", name)
		}
	}
	migration, err := mysql.ParseDSN(get("TEST_MYSQL_DSN"))
	if err != nil || migration.DBName != "" || migration.Net != "tcp" || migration.Addr == "" {
		return nil, errors.New("TEST_MYSQL_DSN must identify a disposable TCP server without a database name")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_]+_e2e$`).MatchString(get("E2E_DB_NAME")) {
		return nil, errors.New("E2E_DB_NAME must end in _e2e, matching the browser test script")
	}
	port, err := strconv.Atoi(get("DB_PORT"))
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("invalid DB_PORT")
	}
	e2e := mysql.NewConfig()
	e2e.User, e2e.Passwd = get("DB_USER"), get("DB_PASSWORD")
	e2e.Net, e2e.Addr = "tcp", net.JoinHostPort(get("DB_HOST"), strconv.Itoa(port))
	result := map[string]*mysql.Config{"migration": migration, "e2e": e2e}
	for _, config := range result {
		config.MultiStatements = false
		config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	}
	return result, nil
}

func probe(config *mysql.Config) error {
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return err
	}
	if one != 1 {
		return errors.New("unexpected connectivity result")
	}
	return nil
}

func main() {
	configs, err := configurations(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, name := range []string{"migration", "e2e"} {
		if err := probe(configs[name]); err != nil {
			// Driver errors may contain credentials or host details. The diagnostic
			// names only the failed probe; operators can inspect their private config.
			fmt.Fprintln(os.Stderr, name+" database connectivity probe failed")
			os.Exit(1)
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"migration": "ok", "e2e": "ok"})
}
