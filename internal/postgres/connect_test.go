package postgres

import "testing"

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"postgres://app:s3cret@db:5432/shop?sslmode=disable": "postgres://app:xxxxx@db:5432/shop?sslmode=disable",
		"postgres://app@db/shop":                             "postgres://app@db/shop",
		"postgres://db/shop?user=app&password=s3cret":        "postgres://db/shop?password=xxxxx&user=app",
		"host=db user=app password=s3cret dbname=shop":       "host=db user=app password=xxxxx dbname=shop",
		`host=db password = 'it\'s secret' dbname=shop`:      "host=db password = xxxxx dbname=shop",
		"host=db user=app dbname=shop":                       "host=db user=app dbname=shop",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got  %q\n want %q", in, got, want)
		}
	}
}
