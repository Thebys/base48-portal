package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type OldUser struct {
	ID                int
	Ident             string
	Email             string
	Realname          sql.NullString
	Phone             sql.NullString
	AltContact        sql.NullString
	Level             sql.NullInt64
	LevelActualAmount string
	PaymentsID        sql.NullInt64
	DateJoined        time.Time
	State             string
	Council           bool
	Staff             bool
}

type OldLevel struct {
	ID     int
	Name   string
	Amount string
	Active bool
}

func main() {
	log.Println("Starting data import from old rememberportal database...")

	// Open old database
	oldDB, err := sql.Open("sqlite", "file:migrations/rememberportal.sqlite3?mode=ro")
	if err != nil {
		log.Fatalf("Failed to open old database: %v", err)
	}
	defer oldDB.Close()

	// Open new database
	newDB, err := sql.Open("sqlite", "file:./data/portal.db?_fk=1")
	if err != nil {
		log.Fatalf("Failed to open new database: %v", err)
	}
	defer newDB.Close()

	// Import levels first
	if err := importLevels(oldDB, newDB); err != nil {
		log.Fatalf("Failed to import levels: %v", err)
	}

	// Import users
	if err := importUsers(oldDB, newDB); err != nil {
		log.Fatalf("Failed to import users: %v", err)
	}

	log.Println("Import completed successfully!")
}

func importLevels(oldDB, newDB *sql.DB) error {
	log.Println("Importing levels...")

	// Get all levels from old database
	rows, err := oldDB.Query("SELECT id, name, amount, active FROM level ORDER BY id")
	if err != nil {
		return fmt.Errorf("query old levels: %w", err)
	}
	defer rows.Close()

	var levels []OldLevel
	for rows.Next() {
		var level OldLevel
		if err := rows.Scan(&level.ID, &level.Name, &level.Amount, &level.Active); err != nil {
			return fmt.Errorf("scan level: %w", err)
		}
		levels = append(levels, level)
	}

	log.Printf("Found %d levels in old database", len(levels))

	// Clear existing levels (except we might want to keep them)
	// Let's just insert/update instead
	for _, level := range levels {
		activeInt := 0
		if level.Active {
			activeInt = 1
		}

		_, err := newDB.Exec(`
			INSERT INTO levels (id, name, amount, active)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name,
				amount = excluded.amount,
				active = excluded.active
		`, level.ID, level.Name, level.Amount, activeInt)

		if err != nil {
			return fmt.Errorf("insert level %d: %w", level.ID, err)
		}
		log.Printf("  ✓ Level %d: %s (%s Kč)", level.ID, level.Name, level.Amount)
	}

	return nil
}

func importUsers(oldDB, newDB *sql.DB) error {
	log.Println("Importing users...")

	// Get all users from old database
	rows, err := oldDB.Query(`
		SELECT
			id, ident, email, realname, phone, altcontact,
			level, level_actual_amount, payments_id, date_joined,
			state, council, staff
		FROM user
		WHERE email NOT LIKE '%@UNKNOWN'
		  AND email NOT LIKE '%@unknown'
		  AND email != ''
		ORDER BY id
	`)
	if err != nil {
		return fmt.Errorf("query old users: %w", err)
	}
	defer rows.Close()

	imported := 0
	skipped := 0

	for rows.Next() {
		var user OldUser
		if err := rows.Scan(
			&user.ID, &user.Ident, &user.Email,
			&user.Realname, &user.Phone, &user.AltContact,
			&user.Level, &user.LevelActualAmount, &user.PaymentsID,
			&user.DateJoined, &user.State, &user.Council, &user.Staff,
		); err != nil {
			return fmt.Errorf("scan user: %w", err)
		}

		// Convert state to lowercase
		state := strings.ToLower(user.State)

		// Set default level if null
		levelID := int64(1)
		if user.Level.Valid {
			levelID = user.Level.Int64
		}

		// Convert payments_id to string
		var paymentsID sql.NullString
		if user.PaymentsID.Valid {
			paymentsID = sql.NullString{
				String: fmt.Sprintf("%d", user.PaymentsID.Int64),
				Valid:  true,
			}
		}

		// Convert boolean to int for SQLite
		isCouncil := 0
		if user.Council {
			isCouncil = 1
		}
		isStaff := 0
		if user.Staff {
			isStaff = 1
		}

		// Insert user (OR IGNORE on duplicate email)
		result, err := newDB.Exec(`
			INSERT OR IGNORE INTO users (
				email, realname, phone, alt_contact,
				level_id, level_actual_amount, payments_id,
				date_joined, state, is_council, is_staff,
				keycloak_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			user.Email, user.Realname, user.Phone, user.AltContact,
			levelID, user.LevelActualAmount, paymentsID,
			user.DateJoined, state, isCouncil, isStaff,
			nil, // NULL keycloak_id, will be linked on first login via LinkKeycloakID
		)

		if err != nil {
			return fmt.Errorf("insert user %s: %w", user.Email, err)
		}

		// Check if row was actually inserted
		rows, _ := result.RowsAffected()
		if rows == 0 {
			// Duplicate email, skipped by OR IGNORE
			skipped++
		} else {
			imported++
			if imported%10 == 0 {
				log.Printf("  ... imported %d users", imported)
			}
		}
	}

	log.Printf("✓ Imported %d users, skipped %d duplicates", imported, skipped)
	return nil
}
