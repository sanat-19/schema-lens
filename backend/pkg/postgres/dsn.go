package postgres

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sanat-19/schema-lens/backend/models"
)

// Fields are the separate boxes of the Connect form.
type Fields struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string
}

// BuildDSN turns form fields into a postgres:// URL. url.UserPassword does
// the escaping, so a password like "p@ss/word?" can't break the URL.
func BuildDSN(f Fields) (string, error) {
	if f.Host == "" || f.Database == "" || f.User == "" {
		return "", fmt.Errorf("host, database and user are required")
	}
	if f.Port == 0 {
		f.Port = 5432
	}
	u := url.URL{
		Scheme: "postgres",
		Host:   f.Host + ":" + strconv.Itoa(f.Port),
		Path:   "/" + f.Database,
	}
	if f.Password != "" {
		u.User = url.UserPassword(f.User, f.Password)
	} else {
		u.User = url.User(f.User)
	}
	if f.SSLMode != "" {
		u.RawQuery = url.Values{"sslmode": {f.SSLMode}}.Encode()
	}
	return u.String(), nil
}

var keywordSSLMode = regexp.MustCompile(`(?i)\bsslmode\s*=\s*(\S+)`)

// Describe reads host, port, database and user out of a connection string,
// for showing and for saving next to a graph. The password is left out.
func Describe(dsn string, schemas []string) (models.Source, error) {
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return models.Source{}, fmt.Errorf("invalid connection string %s", Redact(dsn))
	}
	src := models.Source{
		Host:     cfg.Host,
		Port:     int(cfg.Port),
		Database: cfg.Database,
		User:     cfg.User,
		Schemas:  schemas,
	}
	// pgconn turns sslmode into a TLS config rather than keeping the word,
	// so we read it from the string ourselves.
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			src.SSLMode = u.Query().Get("sslmode")
		}
	} else if m := keywordSSLMode.FindStringSubmatch(dsn); m != nil {
		src.SSLMode = m[1]
	}
	return src, nil
}
