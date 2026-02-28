package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/siofra-seksbot/botster-broker-go/internal/auth"
)

// Group represents a group of agents with a shared admin key.
type Group struct {
	ID           string
	AccountID    string
	Name         string
	AdminKeyHash string
	CreatedAt    string
}

// CreateGroup creates a new group, generates an admin key, and returns the group + plaintext key.
func (db *DB) CreateGroup(accountID, name string) (*Group, string, error) {
	id := generateID()
	plainKey, err := auth.GenerateToken("seks_group")
	if err != nil {
		return nil, "", fmt.Errorf("generate group admin key: %w", err)
	}
	keyHash := auth.HashToken(plainKey)

	_, err = db.Exec(`
		INSERT INTO groups (id, account_id, name, admin_key_hash, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, id, accountID, name, keyHash, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, "", fmt.Errorf("insert group: %w", err)
	}

	g, err := db.GetGroupByID(id)
	if err != nil {
		return nil, "", err
	}
	return g, plainKey, nil
}

// GetGroupByID returns a group by its ID.
func (db *DB) GetGroupByID(id string) (*Group, error) {
	g := &Group{}
	err := db.QueryRow(`
		SELECT id, account_id, name, admin_key_hash, created_at
		FROM groups WHERE id = ?
	`, id).Scan(&g.ID, &g.AccountID, &g.Name, &g.AdminKeyHash, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query group: %w", err)
	}
	return g, nil
}

// GetGroupByAdminKey looks up a group by its plaintext admin key (hashes it first).
func (db *DB) GetGroupByAdminKey(key string) (*Group, error) {
	hash := auth.HashToken(key)
	g := &Group{}
	err := db.QueryRow(`
		SELECT id, account_id, name, admin_key_hash, created_at
		FROM groups WHERE admin_key_hash = ?
	`, hash).Scan(&g.ID, &g.AccountID, &g.Name, &g.AdminKeyHash, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query group by admin key: %w", err)
	}
	return g, nil
}

// ListGroups returns all groups belonging to an account. Passing empty string returns all groups.
func (db *DB) ListGroups(accountID string) ([]*Group, error) {
	var rows *sql.Rows
	var err error
	if accountID == "" {
		rows, err = db.Query(`SELECT id, account_id, name, admin_key_hash, created_at FROM groups ORDER BY created_at`)
	} else {
		rows, err = db.Query(`SELECT id, account_id, name, admin_key_hash, created_at FROM groups WHERE account_id = ? ORDER BY created_at`, accountID)
	}
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	defer rows.Close()

	var groups []*Group
	for rows.Next() {
		g := &Group{}
		if err := rows.Scan(&g.ID, &g.AccountID, &g.Name, &g.AdminKeyHash, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// UpdateGroup updates a group's name.
func (db *DB) UpdateGroup(id, name string) error {
	_, err := db.Exec(`UPDATE groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	return nil
}

// DeleteGroup deletes a group by ID. Agents in the group will have their group_id set to NULL
// (handled by ON DELETE SET NULL is not supported in SQLite; we do it manually).
func (db *DB) DeleteGroup(id string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Unset group_id on all agents in this group
	if _, err := tx.Exec(`UPDATE agents SET group_id = NULL WHERE group_id = ?`, id); err != nil {
		tx.Rollback()
		return fmt.Errorf("unset agent group_ids: %w", err)
	}

	// Delete the group
	if _, err := tx.Exec(`DELETE FROM groups WHERE id = ?`, id); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete group: %w", err)
	}

	return tx.Commit()
}

// RotateGroupAdminKey generates a new admin key for the group and returns the plaintext key.
func (db *DB) RotateGroupAdminKey(id string) (string, error) {
	plainKey, err := auth.GenerateToken("seks_group")
	if err != nil {
		return "", fmt.Errorf("generate group admin key: %w", err)
	}
	keyHash := auth.HashToken(plainKey)
	_, err = db.Exec(`UPDATE groups SET admin_key_hash = ? WHERE id = ?`, keyHash, id)
	if err != nil {
		return "", fmt.Errorf("rotate group admin key: %w", err)
	}
	return plainKey, nil
}

// AssignAgentToGroup assigns an agent to a group (or clears with empty groupID).
func (db *DB) AssignAgentToGroup(agentID, groupID string) error {
	var err error
	if groupID == "" {
		_, err = db.Exec(`UPDATE agents SET group_id = NULL WHERE id = ?`, agentID)
	} else {
		_, err = db.Exec(`UPDATE agents SET group_id = ? WHERE id = ?`, groupID, agentID)
	}
	if err != nil {
		return fmt.Errorf("assign agent to group: %w", err)
	}
	return nil
}

// GetAgentsByGroup returns all agents belonging to a specific group.
func (db *DB) GetAgentsByGroup(groupID string) ([]*Agent, error) {
	rows, err := db.Query(`SELECT `+agentColumns+` FROM agents WHERE group_id = ? ORDER BY created_at`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query agents by group: %w", err)
	}
	defer rows.Close()

	var agents []*Agent
	for rows.Next() {
		a := &Agent{}
		if err := rows.Scan(&a.ID, &a.AccountID, &a.Name, &a.TokenHash, &a.EncryptedToken,
			&a.Safe, &a.SelectedActuatorID, &a.Role, &a.GroupID, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}
