package netflix

import (
	"encoding/binary"
	"errors"
)

type wireField struct {
	number int
	value  uint64
	data   []byte
}

func wireFields(data []byte) ([]wireField, error) {
	var fields []wireField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 {
			return nil, errors.New("etiqueta protobuf inválida")
		}
		data = data[n:]
		field := wireField{number: int(tag >> 3)}
		switch tag & 7 {
		case 0:
			value, n := binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("entero protobuf inválido")
			}
			field.value = value
			data = data[n:]
		case 1:
			if len(data) < 8 {
				return nil, errors.New("campo protobuf truncado")
			}
			data = data[8:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data)-n) {
				return nil, errors.New("campo protobuf truncado")
			}
			data = data[n:]
			field.data = data[:int(size)]
			data = data[int(size):]
		case 5:
			if len(data) < 4 {
				return nil, errors.New("campo protobuf truncado")
			}
			data = data[4:]
		default:
			return nil, errors.New("tipo protobuf no compatible")
		}
		fields = append(fields, field)
		if len(fields) > 10000 {
			return nil, errors.New("demasiados campos protobuf")
		}
	}
	return fields, nil
}

type ContentPolicy struct {
	SecurityLevel uint64 `json:"securityLevel"`
	RequiredHDCP  uint64 `json:"requiredHDCP"`
	AntiRollback  bool   `json:"antiRollback"`
	HasKeyControl bool   `json:"hasKeyControl"`
}
type LicensePolicy struct {
	CanPlay    bool            `json:"canPlay"`
	CanPersist bool            `json:"canPersist"`
	Content    []ContentPolicy `json:"content"`
}

// LicenseRequirements reads only public policy fields. Encrypted key bytes,
// signatures and identifiers are never returned or logged.
func LicenseRequirements(signed []byte) (LicensePolicy, error) {
	var policy LicensePolicy
	outer, err := wireFields(signed)
	if err != nil {
		return policy, err
	}
	var payload []byte
	for _, field := range outer {
		if field.number == 2 {
			payload = field.data
			break
		}
	}
	fields, err := wireFields(payload)
	if err != nil {
		return policy, err
	}
	for _, field := range fields {
		if field.number == 2 {
			values, err := wireFields(field.data)
			if err != nil {
				return policy, err
			}
			for _, value := range values {
				if value.number == 1 {
					policy.CanPlay = value.value != 0
				}
				if value.number == 2 {
					policy.CanPersist = value.value != 0
				}
			}
		}
		if field.number != 3 {
			continue
		}
		keyFields, err := wireFields(field.data)
		if err != nil {
			return policy, err
		}
		content := ContentPolicy{SecurityLevel: 1}
		var kind uint64
		for _, value := range keyFields {
			switch value.number {
			case 4:
				kind = value.value
			case 5:
				content.SecurityLevel = value.value
			case 6:
				protection, err := wireFields(value.data)
				if err != nil {
					return policy, err
				}
				for _, setting := range protection {
					if setting.number == 1 {
						content.RequiredHDCP = setting.value
					}
				}
			case 8:
				content.HasKeyControl = len(value.data) > 0
			case 11:
				content.AntiRollback = value.value != 0
			}
		}
		if kind == 2 {
			policy.Content = append(policy.Content, content)
		}
	}
	return policy, nil
}
