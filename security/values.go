/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package security

import "fmt"

// ProcessSecureValue hashes or encrypts a value based on the column's security kind.
// kind "hash" produces a bcrypt hash; kind "encrypt" uses AES encryption.
func ProcessSecureValue(kind string, value interface{}, enc *Encryptor) (interface{}, error) {
	strVal := fmt.Sprintf("%v", value)
	switch kind {
	case "hash":
		hashed, err := HashPassword(strVal)
		if err != nil {
			return nil, fmt.Errorf("hashing password: %w", err)
		}
		return hashed, nil
	case "encrypt":
		if enc == nil {
			return nil, fmt.Errorf("encryption not configured")
		}
		encrypted, err := enc.Encrypt(strVal)
		if err != nil {
			return nil, fmt.Errorf("encrypting value: %w", err)
		}
		return encrypted, nil
	default:
		return value, nil
	}
}
