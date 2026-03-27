/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package models

import (
	"database/sql"
	"fmt"
	"time"
)

type Site struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Domain      *string   `json:"domain"`
	Description *string   `json:"description"`
	LLMModelID  int       `json:"llm_model_id"`
	Status      string    `json:"status"`
	Mode        string    `json:"mode"`
	Config      string    `json:"config"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const siteColumns = "id, name, domain, description, llm_model_id, status, mode, config, created_at, updated_at"

func scanSite(s *Site, row interface{ Scan(...interface{}) error }) error {
	return row.Scan(&s.ID, &s.Name, &s.Domain, &s.Description, &s.LLMModelID, &s.Status, &s.Mode, &s.Config, &s.CreatedAt, &s.UpdatedAt)
}

func CreateSite(db *sql.DB, name string, domain, description *string, llmModelID int) (*Site, error) {
	result, err := db.Exec(
		"INSERT INTO sites (name, domain, description, llm_model_id, status, mode) VALUES (?, ?, ?, ?, 'active', 'building')",
		name, domain, description, llmModelID,
	)
	if err != nil {
		return nil, err
	}

	id, _ := result.LastInsertId()
	return GetSiteByID(db, int(id))
}

func GetSiteByID(db *sql.DB, id int) (*Site, error) {
	s := &Site{}
	err := scanSite(s, db.QueryRow("SELECT "+siteColumns+" FROM sites WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func GetSiteByDomain(db *sql.DB, domain string) (*Site, error) {
	s := &Site{}
	err := scanSite(s, db.QueryRow("SELECT "+siteColumns+" FROM sites WHERE domain = ?", domain))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func ListSites(db *sql.DB) ([]Site, error) {
	rows, err := db.Query("SELECT " + siteColumns + " FROM sites ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		if err := scanSite(&s, rows); err != nil {
			return nil, err
		}
		sites = append(sites, s)
	}
	return sites, nil
}

// ListSitesPaginated returns a page of sites with the total count.
func ListSitesPaginated(db *sql.DB, limit, offset int) ([]Site, int, error) {
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM sites").Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := db.Query("SELECT "+siteColumns+" FROM sites ORDER BY id LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		if err := scanSite(&s, rows); err != nil {
			return nil, 0, err
		}
		sites = append(sites, s)
	}
	return sites, total, nil
}

func ListActiveSites(db *sql.DB) ([]Site, error) {
	rows, err := db.Query("SELECT " + siteColumns + " FROM sites WHERE status = 'active' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		if err := scanSite(&s, rows); err != nil {
			return nil, err
		}
		sites = append(sites, s)
	}
	return sites, nil
}

func UpdateSite(db *sql.DB, id int, name string, domain, description *string, llmModelID int) error {
	_, err := db.Exec(
		"UPDATE sites SET name = ?, domain = ?, description = ?, llm_model_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		name, domain, description, llmModelID, id,
	)
	return err
}

func UpdateSiteDescription(db *sql.DB, id int, description *string) error {
	_, err := db.Exec(
		"UPDATE sites SET description = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		description, id,
	)
	return err
}

func UpdateSiteStatus(db *sql.DB, id int, status string) error {
	_, err := db.Exec(
		"UPDATE sites SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		status, id,
	)
	return err
}

func UpdateSiteMode(db *sql.DB, id int, mode string) error {
	_, err := db.Exec(
		"UPDATE sites SET mode = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		mode, id,
	)
	return err
}

// DeleteSite removes the site row from the global database.
// Per-site DB and file cleanup is handled by SiteDBManager.Delete() in the admin handler.
func DeleteSite(db *sql.DB, id int) error {
	res, err := db.Exec("DELETE FROM sites WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("site %d not found", id)
	}
	return nil
}
