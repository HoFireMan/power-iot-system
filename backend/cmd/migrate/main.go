// Command migrate applies or rolls back the versioned PostgreSQL/TimescaleDB
// schema. It is intentionally separate from GORM model lifecycle operations.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"power-iot-backend/internal/data/migrations"
)

func main() {
	databaseURL := flag.String("database-url", strings.TrimSpace(os.Getenv("DATABASE_URL")), "PostgreSQL connection URL")
	direction := flag.String("direction", "up", "migration action: up, down, or version")
	flag.Parse()
	if strings.TrimSpace(*databaseURL) == "" {
		log.Fatal("DATABASE_URL or -database-url is required")
	}

	switch strings.ToLower(strings.TrimSpace(*direction)) {
	case "up":
		if err := migrations.Bootstrap(*databaseURL); err != nil {
			log.Fatal(err)
		}
	case "down":
		if err := migrations.Down(*databaseURL); err != nil {
			log.Fatal(err)
		}
	case "version":
		version, dirty, err := migrations.Version(*databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("version=%d dirty=%t\n", version, dirty)
	default:
		log.Fatalf("unsupported migration action %q", *direction)
	}
}
