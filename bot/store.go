/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package bot

import "github.com/markdr-hue/HO/db"

// LoadSessions restores bot user sessions from the database.
func LoadSessions(database *db.DB) map[string]*UserState {
	sessions := make(map[string]*UserState)
	rows, err := database.DB.Query("SELECT provider, chat_id, user_id, active_site_id, muted FROM bot_sessions")
	if err != nil {
		return sessions
	}
	defer rows.Close()

	for rows.Next() {
		var provider, chatID, userID string
		var activeSiteID int
		var muted bool
		if rows.Scan(&provider, &chatID, &userID, &activeSiteID, &muted) == nil {
			key := provider + ":" + chatID
			sessions[key] = &UserState{
				Provider:     provider,
				ChatID:       chatID,
				UserID:       userID,
				ActiveSiteID: activeSiteID,
				Muted:        muted,
			}
		}
	}
	return sessions
}

// SaveSession persists a user's session state.
func SaveSession(database *db.DB, state *UserState) {
	database.ExecWrite(
		`INSERT INTO bot_sessions (provider, chat_id, user_id, active_site_id, muted, updated_at)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(provider, chat_id) DO UPDATE SET
		   user_id = excluded.user_id,
		   active_site_id = excluded.active_site_id,
		   muted = excluded.muted,
		   updated_at = CURRENT_TIMESTAMP`,
		state.Provider, state.ChatID, state.UserID, state.ActiveSiteID, state.Muted,
	)
}

// DeleteSession removes a user's session.
func DeleteSession(database *db.DB, provider, chatID string) {
	database.ExecWrite("DELETE FROM bot_sessions WHERE provider = ? AND chat_id = ?", provider, chatID)
}
