package duckdb

/*
#include <duckdb.h>
*/
import "C"

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/mitchellh/mapstructure"
)

type numericType interface {
	int | int8 | int16 | int32 | int64 | uint | uint8 | uint16 | uint32 | uint64 | float32 | float64
}

const uuid_length = 16

type UUID [uuid_length]byte

func (u *UUID) Scan(v any) error {
	switch val := v.(type) {
	case []byte:
		if len(val) != uuid_length {
			return u.Scan(string(val))
		}
		copy(u[:], val[:])
	case string:
		id, err := uuid.Parse(val)
		if err != nil {
			return err
		}
		copy(u[:], id[:])
	default:
		return fmt.Errorf("invalid UUID value type: %T", val)
	}
	return nil
}

func (u *UUID) String() string {
	buf := make([]byte, 36)

	hex.Encode(buf, u[:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], u[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], u[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], u[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:], u[10:])

	return string(buf)
}

// duckdb_hugeint is composed of (lower, upper) components.
// The value is computed as: upper * 2^64 + lower

func hugeIntToUUID(hi C.duckdb_hugeint) []byte {
	var uuid [uuid_length]byte
	// We need to flip the sign bit of the signed hugeint to transform it to UUID bytes
	binary.BigEndian.PutUint64(uuid[:8], uint64(hi.upper)^1<<63)
	binary.BigEndian.PutUint64(uuid[8:], uint64(hi.lower))
	return uuid[:]
}

func uuidToHugeInt(uuid UUID) C.duckdb_hugeint {
	var dt C.duckdb_hugeint
	upper := binary.BigEndian.Uint64(uuid[:8])
	// flip the sign bit
	upper = upper ^ (1 << 63)
	dt.upper = C.int64_t(upper)
	dt.lower = C.uint64_t(binary.BigEndian.Uint64(uuid[8:]))
	return dt
}

func hugeIntToNative(hi C.duckdb_hugeint) *big.Int {
	i := big.NewInt(int64(hi.upper))
	i.Lsh(i, 64)
	i.Add(i, new(big.Int).SetUint64(uint64(hi.lower)))
	return i
}

func hugeIntFromNative(i *big.Int) (C.duckdb_hugeint, error) {
	d := big.NewInt(1)
	d.Lsh(d, 64)

	q := new(big.Int)
	r := new(big.Int)
	q.DivMod(i, d, r)

	if !q.IsInt64() {
		return C.duckdb_hugeint{}, fmt.Errorf("big.Int(%s) is too big for HUGEINT", i.String())
	}

	return C.duckdb_hugeint{
		lower: C.uint64_t(r.Uint64()),
		upper: C.int64_t(q.Int64()),
	}, nil
}

type Map map[any]any

func (m *Map) Scan(v any) error {
	data, ok := v.(Map)
	if !ok {
		return fmt.Errorf("invalid type `%T` for scanning `Map`, expected `Map`", data)
	}

	*m = data
	return nil
}

func mapKeysField() string {
	return "key"
}

func mapValuesField() string {
	return "value"
}

type Interval struct {
	Days   int32 `json:"days"`
	Months int32 `json:"months"`
	Micros int64 `json:"micros"`
}

// Use as the `Scanner` type for any composite types (maps, lists, structs)
type Composite[T any] struct {
	t T
}

func (s Composite[T]) Get() T {
	return s.t
}

func (s *Composite[T]) Scan(v any) error {
	return mapstructure.Decode(v, &s.t)
}

const max_decimal_width = 38

type Decimal struct {
	Width uint8
	Scale uint8
	Value *big.Int
}

func (d *Decimal) Float64() float64 {
	scale := big.NewInt(int64(d.Scale))
	factor := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), scale, nil))
	value := new(big.Float).SetInt(d.Value)
	value.Quo(value, factor)
	f, _ := value.Float64()
	return f
}

func (d *Decimal) String() string {
	// Get the sign, and return early if zero
	if d.Value.Sign() == 0 {
		return "0"
	}

	// Remove the sign from the string integer value
	var signStr string
	scaleless := d.Value.String()
	if d.Value.Sign() < 0 {
		signStr = "-"
		scaleless = scaleless[1:]
	}

	// Remove all zeros from the right side
	zeroTrimmed := strings.TrimRightFunc(scaleless, func(r rune) bool { return r == '0' })
	scale := int(d.Scale) - (len(scaleless) - len(zeroTrimmed))

	// If the string is still bigger than the scale factor, output it without a decimal point
	if scale <= 0 {
		return signStr + zeroTrimmed + strings.Repeat("0", -1*scale)
	}

	// Pad a number with 0.0's if needed
	if len(zeroTrimmed) <= scale {
		return fmt.Sprintf("%s0.%s%s", signStr, strings.Repeat("0", scale-len(zeroTrimmed)), zeroTrimmed)
	}
	return signStr + zeroTrimmed[:len(zeroTrimmed)-scale] + "." + zeroTrimmed[len(zeroTrimmed)-scale:]
}
