package database

import (
	"log"

	"gorm.io/gorm"
	"store-front/apps/api/internal/models"
)

func SeedCountries(db *gorm.DB) error {
	countries := []string{
		"Malaysia",
	}

	for _, name := range countries {
		var count int64
		db.Model(&models.Country{}).Where("name = ?", name).Count(&count)
		if count > 0 {
			log.Printf("Country '%s' already exists, skipping...", name)
			continue
		}

		country := models.Country{Name: name}
		if err := db.Create(&country).Error; err != nil {
			log.Printf("Warning: failed to create country %s: %v", name, err)
			continue
		}
		log.Printf("Created country: %s", name)
	}

	return nil
}
