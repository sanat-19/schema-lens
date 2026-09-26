package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExitCodes(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no command", nil, exitUsage},
		{"unknown command", []string{"frobnicate"}, exitUsage},
		{"help", []string{"help"}, exitOK},
		{"flag help", []string{"export", "-h"}, exitOK},
		{"no database given", []string{"export"}, exitUsage},
		{"bad format", []string{"export", "--dsn", "postgres://x@127.0.0.1:1/db", "--format", "xml"}, exitUsage},
		{"snapshot without -o", []string{"snapshot", "--dsn", "postgres://x@127.0.0.1:1/db"}, exitUsage},
		{"unreachable database", []string{"export", "--dsn", "postgres://x:pw@127.0.0.1:1/db?connect_timeout=2"}, exitFailure},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		if got := run(c.args, &stdout, &stderr); got != c.want {
			t.Errorf("%s: exit %d, want %d (stderr: %s)", c.name, got, c.want, stderr.String())
		}
	}
}

func TestPasswordNeverPrinted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run([]string{"export", "--dsn", "postgres://app:hunter2@127.0.0.1:1/db?connect_timeout=2"}, &stdout, &stderr)

	if strings.Contains(stderr.String(), "hunter2") {
		t.Errorf("password leaked into error output: %s", stderr.String())
	}
}
