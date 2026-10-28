package main

import (
	"database/sql"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/tuzi/cdk-recharge-system/internal/cardplatform"
	"github.com/tuzi/cdk-recharge-system/internal/cdklookup"
)

func main() {
	// Read only the administration DB; never initialize/migrate it or copy
	// the owner's key into the public service's database/environment.
	path, socket := os.Getenv("CDK_LOOKUP_DB"), os.Getenv("CDK_LOOKUP_SOCKET")
	if path == "" || socket == "" {
		log.Fatal("CDK_LOOKUP_DB and CDK_LOOKUP_SOCKET are required")
	}
	u := &url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1&_busy_timeout=5000"}
	database, err := sql.Open("sqlite3", u.String())
	if err != nil {
		log.Fatal("cannot open lookup configuration")
	}
	defer database.Close()
	load := func() cardplatform.Config {
		cfg := cardplatform.Config{}
		_ = database.QueryRow("SELECT value FROM site_settings WHERE key='card_api_base'").Scan(&cfg.SiteBase)
		_ = database.QueryRow("SELECT value FROM site_settings WHERE key='card_api_key'").Scan(&cfg.APIKey)
		cfg.SiteBase = strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(cfg.SiteBase, "/"), "/openapi/v1"), "/openapi")
		if cfg.SiteBase == "" {
			cfg.SiteBase = "https://zovocard.com"
		}
		if cfg.SiteBase != "https://zovocard.com" && cfg.SiteBase != "https://sandbox.zovocard.com" || cfg.APIKey == "" {
			log.Fatal("lookup requires an allowed upstream and an owner key in the administration database")
		}
		return cfg
	}
	upstream := cardplatform.NewReadOnly(load())
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatal("cannot create lookup socket")
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0660); err != nil {
		log.Fatal("cannot set lookup socket permissions")
	}
	server := &http.Server{Handler: cdklookup.Handler(upstream), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 4096}
	log.Print("read-only CDK lookup ready on Unix socket")
	log.Fatal(server.Serve(listener))
}
