package postgres

import (
	"strings"
	"testing"
)

func TestBuildDSNEscapesThePassword(t *testing.T) {
	dsn, err := BuildDSN(Fields{Host: "db.internal", Database: "shop", User: "app", Password: "p@ss/wo:rd?#", SSLMode: "require"})
	if err != nil {
		t.Fatal(err)
	}
	if dsn != "postgres://app:p%40ss%2Fwo%3Ard%3F%23@db.internal:5432/shop?sslmode=require" {
		t.Errorf("unexpected DSN %s", dsn)
	}

	// And it must read back as the same connection.
	src, err := Describe(dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if src.Host != "db.internal" || src.Port != 5432 || src.Database != "shop" || src.User != "app" || src.SSLMode != "require" {
		t.Errorf("round trip lost something: %+v", src)
	}
}

func TestBuildDSNNeedsTheBasics(t *testing.T) {
	if _, err := BuildDSN(Fields{Host: "db", User: "app"}); err == nil {
		t.Error("a DSN without a database should be refused")
	}
}

func TestDescribeKeywordStyle(t *testing.T) {
	src, err := Describe("host=db port=6543 dbname=shop user=app password=secret sslmode=disable", []string{"public"})
	if err != nil {
		t.Fatal(err)
	}
	if src.Host != "db" || src.Port != 6543 || src.Database != "shop" || src.SSLMode != "disable" || src.Schemas[0] != "public" {
		t.Errorf("unexpected source %+v", src)
	}
}

func TestDescribeNeverEchoesThePassword(t *testing.T) {
	_, err := Describe("postgres://app:hunter2@db:notaport/shop", nil)
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("bad DSN error must not contain the password: %v", err)
	}
}
