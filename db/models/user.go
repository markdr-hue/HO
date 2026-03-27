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

type User struct {
	ID           int       `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func CreateUser(db *sql.DB, username, passwordHash, role, displayName string) (*User, error) {
	return CreateUserTx(db, username, passwordHash, role, displayName)
}

// CreateUserTx creates a user using any Executor (DB or Tx).
func CreateUserTx(exec Executor, username, passwordHash, role, displayName string) (*User, error) {
	result, err := exec.Exec(
		"INSERT INTO users (username, password_hash, role, display_name) VALUES (?, ?, ?, ?)",
		username, passwordHash, role, displayName,
	)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}
	return &User{
		ID:          int(id),
		Username:    username,
		DisplayName: displayName,
		Role:        role,
	}, nil
}

func GetUserByUsername(db *sql.DB, username string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		"SELECT id, username, password_hash, display_name, role, created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func GetUserByID(db *sql.DB, id int) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		"SELECT id, username, password_hash, display_name, role, created_at, updated_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func ListUsers(db *sql.DB) ([]User, error) {
	rows, err := db.Query("SELECT id, username, display_name, role, created_at, updated_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

func CountUsers(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

func UpdateUserPassword(db *sql.DB, id int, passwordHash string) error {
	_, err := db.Exec(
		"UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		passwordHash, id,
	)
	return err
}

func UpdateUserRole(db *sql.DB, id int, role string) error {
	_, err := db.Exec(
		"UPDATE users SET role = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		role, id,
	)
	return err
}

func UpdateUserDisplayName(db *sql.DB, id int, displayName string) error {
	_, err := db.Exec(
		"UPDATE users SET display_name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?",
		displayName, id,
	)
	return err
}

func DeleteUser(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

// GetOwnerDisplayName returns the display name of the first admin user (the owner).
func GetOwnerDisplayName(db *sql.DB) string {
	var name string
	err := db.QueryRow("SELECT display_name FROM users WHERE role = 'admin' ORDER BY id LIMIT 1").Scan(&name)
	if err != nil {
		return ""
	}
	return name
}
