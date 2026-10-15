package cmd

import (
	"net/url"
	"strings"

	"github.com/axitdev/onfly/internal/config"
)

func dbLabel(db config.Database) string {
	if db.Driver != "postgres" {
		return db.Driver + " " + db.DSN
	}
	u, err := url.Parse(db.DSN)
	if err != nil || u.Host == "" {
		return "postgres"
	}
	return "postgres " + u.Host + "/" + strings.TrimPrefix(u.Path, "/")
}
