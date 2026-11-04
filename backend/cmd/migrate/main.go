// Command migrate applies SQL migrations: "up", "down <n>", "version".
package main

import (
	"fmt"
	"os"
	"strconv"

	"beeloy.com/backend/internal/platform/migrate"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	dir := os.Getenv("MIGRATIONS_DIR")
	if dir == "" {
		dir = "../migrations"
		if _, err := os.Stat("/migrations"); err == nil {
			dir = "/migrations"
		}
	}
	if dbURL == "" || len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: DATABASE_URL=... [MIGRATIONS_DIR=...] migrate up | down <n> | version")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "up":
		err = migrate.Up(dbURL, dir)
	case "down":
		n := 0
		if len(os.Args) >= 3 {
			n, _ = strconv.Atoi(os.Args[2])
		}
		err = migrate.Down(dbURL, dir, n)
	case "version":
		var v uint
		var dirty bool
		v, dirty, err = migrate.Version(dbURL, dir)
		if err == nil {
			fmt.Printf("version=%d dirty=%v\n", v, dirty)
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}
