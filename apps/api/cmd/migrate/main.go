package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"store-front/apps/api/internal/config"
	"store-front/apps/api/internal/database"
	"store-front/apps/api/internal/models"
)

func main() {
	fresh := flag.Bool("fresh", false, "Drop all tables before migrating")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	if *fresh {
		fmt.Println("Dropping all tables...")
		if err := database.DropAll(db); err != nil {
			log.Fatalf("Failed to drop tables: %v", err)
		}
		fmt.Println("All tables dropped.")
	}

	// Empty webhook external ids become NULL before the unique index is built.
	//
	// The unique index on (provider, external_id) used to be declared in a
	// method nothing called, so a project from before that was fixed can hold
	// several rows with external_id = "", and no database will build a unique
	// index over repeated empty strings. NULL repeats freely, and an event the
	// provider gave no id was never deduplicable anyway.
	//
	// Here rather than inside models.Migrate because this file is rewritten by
	// grit upgrade and models/user.go is not: that one holds the model registry
	// people add to. A migration fix living in a file upgrades never touch is a
	// fix only new projects get.
	if db.Migrator().HasTable("webhook_events") {
		db.Exec("UPDATE webhook_events SET external_id = NULL WHERE external_id = ''")
	}

	fmt.Println("Running migrations...")
	if err := models.Migrate(db); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	fmt.Println("Migrations completed successfully.")
	os.Exit(0)
}
