package database

import (
	"log"

	"gorm.io/gorm"
	"store-front/apps/api/internal/models"
)

// SeedStates creates the states for Malaysia. The country must already exist.
func SeedStates(db *gorm.DB) error {
	var country models.Country
	if err := db.Where("name = ?", "Malaysia").First(&country).Error; err != nil {
		log.Println("Country 'Malaysia' not found, skipping state seeding...")
		return nil
	}

	return seedMalaysianStates(db, country.ID)
}

func seedMalaysianStates(db *gorm.DB, countryID string) error {
	states := []string{
		"Johor",
		"Kedah",
		"Kelantan",
		"Melaka",
		"Negeri Sembilan",
		"Pahang",
		"Penang",
		"Perak",
		"Perlis",
		"Sabah",
		"Sarawak",
		"Selangor",
		"Terengganu",
		"Wilayah Persekutuan Kuala Lumpur",
		"Wilayah Persekutuan Labuan",
		"Wilayah Persekutuan Putrajaya",
	}

	for _, name := range states {
		var count int64
		db.Model(&models.State{}).Where("name = ? AND country_id = ?", name, countryID).Count(&count)
		if count > 0 {
			continue
		}

		state := models.State{Name: name, CountryID: countryID}
		if err := db.Create(&state).Error; err != nil {
			log.Printf("Warning: failed to create state %s: %v", name, err)
			continue
		}
		log.Printf("Created state: %s", name)
	}

	return nil
}
