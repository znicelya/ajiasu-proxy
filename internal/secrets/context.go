package secrets

import (
	"encoding/binary"
)

const contextVersion = byte(1)

func authenticatedContext(value Context, layer string) ([]byte, error) {
	if !value.Valid() || layer == "" || len(layer) > 64 {
		return nil, ErrInvalidContext
	}
	purpose := []byte(value.Purpose)
	layerBytes := []byte(layer)
	result := make([]byte, 1+16+16+8+2+len(purpose)+2+len(layerBytes))
	result[0] = contextVersion
	copy(result[1:17], value.TenantID[:])
	copy(result[17:33], value.AccountID[:])
	binary.BigEndian.PutUint64(result[33:41], uint64(value.Version))
	binary.BigEndian.PutUint16(result[41:43], uint16(len(purpose)))
	offset := 43
	copy(result[offset:offset+len(purpose)], purpose)
	offset += len(purpose)
	binary.BigEndian.PutUint16(result[offset:offset+2], uint16(len(layerBytes)))
	offset += 2
	copy(result[offset:], layerBytes)
	return result, nil
}
