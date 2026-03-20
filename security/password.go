/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package security

import "golang.org/x/crypto/bcrypt"

func HashPassword(password string) (string, error) {
	// Cost 10 ≈ ~100ms per hash — good balance of security and responsiveness.
	// Cost 14 was causing 2-4s per hash, blocking auth under concurrent load.
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	return string(bytes), err
}

func CheckPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
