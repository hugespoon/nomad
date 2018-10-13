package structs

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"github.com/hashicorp/nomad/helper"
)

const (
	// floatPrecision is the precision used before rounding. It is set to a high
	// number to give a high chance of correctly returning equality.
	floatPrecision = uint(256)
)

// BaseUnit is a unique base unit. All units that share the same base unit
// should be comparable.
type BaseUnit uint16

const (
	UnitScalar BaseUnit = iota
	UnitByte
	UnitByteRate
	UnitHertz
	UnitWatt
)

// Unit describes a unit and its multiplier over the base unit type
type Unit struct {
	// Name is the name of the unit (GiB, MB/s)
	Name string

	// Base is the base unit for the unit
	Base BaseUnit

	// Multiplier is the multiplier over the base unit (KiB multiplier is 1024)
	Multiplier int64

	// InverseMultiplier specifies that the multiplier is an inverse so:
	// Base / Multiplier. For example a mW is a W/1000.
	InverseMultiplier bool
}

// Comparable returns if two units are comparable
func (u *Unit) Comparable(o *Unit) bool {
	if u == nil || o == nil {
		return false
	}

	return u.Base == o.Base
}

// Attribute is used to describe the value of an attribute, optionally
// specifying units
type Attribute struct {
	// Float is the float value for the attribute
	Float *float64

	// Int is the int value for the attribute
	Int *int64

	// String is the string value for the attribute
	String *string

	// Bool is the bool value for the attribute
	Bool *bool

	// Unit is the optional unit for the set int or float value
	Unit string
}

func (a *Attribute) Copy() *Attribute {
	ca := &Attribute{
		Unit: a.Unit,
	}

	if a.Float != nil {
		ca.Float = helper.Float64ToPtr(*a.Float)
	}
	if a.Int != nil {
		ca.Int = helper.Int64ToPtr(*a.Int)
	}
	if a.Bool != nil {
		ca.Bool = helper.BoolToPtr(*a.Bool)
	}
	if a.String != nil {
		ca.String = helper.StringToPtr(*a.String)
	}

	return ca
}

// GoString returns a string representation of the attribute
func (a *Attribute) GoString() string {
	if a == nil {
		return "nil attribute"
	}

	var b strings.Builder
	if a.Float != nil {
		b.WriteString(fmt.Sprintf("%v", *a.Float))
	} else if a.Int != nil {
		b.WriteString(fmt.Sprintf("%v", *a.Int))
	} else if a.Bool != nil {
		b.WriteString(fmt.Sprintf("%v", *a.Bool))
	} else if a.String != nil {
		b.WriteString(*a.String)
	}

	if a.Unit != "" {
		b.WriteString(a.Unit)
	}

	return b.String()
}

// Validate checks if the attribute is valid
func (a *Attribute) Validate() error {
	if a.Unit != "" {
		if _, ok := UnitIndex[a.Unit]; !ok {
			return fmt.Errorf("unrecognized unit %q", a.Unit)
		}

		// Check only int/float set
		if a.String != nil || a.Bool != nil {
			return fmt.Errorf("unit can not be specified on a boolean or string attribute")
		}
	}

	// Assert only one of the attributes is set
	set := 0
	if a.Float != nil {
		set++
	}
	if a.Int != nil {
		set++
	}
	if a.String != nil {
		set++
	}
	if a.Bool != nil {
		set++
	}

	if set == 0 {
		return fmt.Errorf("no attribute value set")
	} else if set > 1 {
		return fmt.Errorf("only one attribute value may be set")
	}

	return nil
}

// Compare compares two attributes. If the returned boolean value is false, it
// means the values are not comparable, either because they are of different
// types (bool versus int) or the units are incompatible for comparison.
// The returned int will be 0 if a==b, -1 if a < b, and +1 if a > b for all
// values but bool. For bool it will be 0 if a==b or 1 if a!=b.
func (a *Attribute) Compare(b *Attribute) (int, bool) {
	if !a.Comparable(b) {
		return 0, false
	}

	return a.comparitor()(b)
}

// comparitor returns the comparitor function for the attribute
func (a *Attribute) comparitor() compareFn {
	if a.Bool != nil {
		return a.boolComparitor
	}
	if a.String != nil {
		return a.stringComparitor
	}
	if a.Int != nil || a.Float != nil {
		return a.numberComparitor
	}

	return nullComparitor
}

// boolComparitor compares two boolean attributes
func (a *Attribute) boolComparitor(b *Attribute) (int, bool) {
	if *a.Bool == *b.Bool {
		return 0, true
	}

	return 1, true
}

// stringComparitor compares two string attributes
func (a *Attribute) stringComparitor(b *Attribute) (int, bool) {
	return strings.Compare(*a.String, *b.String), true
}

// numberComparitor compares two number attributes, having either Int or Float
// set.
func (a *Attribute) numberComparitor(b *Attribute) (int, bool) {
	// If they are both integers we do perfect precision comparisons
	if a.Int != nil && b.Int != nil {
		return a.intComparitor(b)
	}

	// Push both into the float space
	af := a.getBigFloat()
	bf := b.getBigFloat()
	if af == nil || bf == nil {
		return 0, false
	}

	return af.Cmp(bf), true
}

// intComparitor compares two integer attributes.
func (a *Attribute) intComparitor(b *Attribute) (int, bool) {
	ai := a.getInt()
	bi := b.getInt()

	if ai == bi {
		return 0, true
	} else if ai < bi {
		return -1, true
	} else {
		return 1, true
	}
}

// nullComparitor always returns false and is used when no comparison function
// is possible
func nullComparitor(*Attribute) (int, bool) {
	return 0, false
}

// compareFn is used to compare two attributes. It returns -1, 0, 1 for ordering
// and a boolean for if the comparison is possible.
type compareFn func(b *Attribute) (int, bool)

// getBigFloat returns a big.Float representation of the attribute, converting
// the value to the base unit if a unit is specified.
func (a *Attribute) getBigFloat() *big.Float {
	f := new(big.Float)
	f.SetPrec(floatPrecision)
	if a.Int != nil {
		f.SetInt64(*a.Int)
	} else if a.Float != nil {
		f.SetFloat64(*a.Float)
	} else {
		return nil
	}

	// Get the unit
	u := a.getTypedUnit()

	// If there is no unit just return the float
	if u == nil {
		return f
	}

	// Convert to the base unit
	multiplier := new(big.Float)
	multiplier.SetPrec(floatPrecision)
	multiplier.SetInt64(u.Multiplier)
	if u.InverseMultiplier {
		base := big.NewFloat(1.0)
		base.SetPrec(floatPrecision)
		multiplier = multiplier.Quo(base, multiplier)
	}

	f.Mul(f, multiplier)
	return f
}

// getInt returns an int representation of the attribute, converting
// the value to the base unit if a unit is specified.
func (a *Attribute) getInt() int64 {
	if a.Int == nil {
		return 0
	}

	i := *a.Int

	// Get the unit
	u := a.getTypedUnit()

	// If there is no unit just return the int
	if u == nil {
		return i
	}

	if u.InverseMultiplier {
		i /= u.Multiplier
	} else {
		i *= u.Multiplier
	}

	return i
}

// Comparable returns whether they are comparable
func (a *Attribute) Comparable(b *Attribute) bool {
	if a == nil || b == nil {
		return false
	}

	// First use the units to decide if comparison is possible
	aUnit := a.getTypedUnit()
	bUnit := b.getTypedUnit()
	if aUnit != nil && bUnit != nil {
		return aUnit.Comparable(bUnit)
	} else if aUnit != nil && bUnit == nil {
		return false
	} else if aUnit == nil && bUnit != nil {
		return false
	}

	if a.String != nil {
		if b.String != nil {
			return true
		}
		return false
	}
	if a.Bool != nil {
		if b.Bool != nil {
			return true
		}
		return false
	}

	return true
}

// getTypedUnit returns the Unit for the attribute or nil if no unit exists.
func (a *Attribute) getTypedUnit() *Unit {
	return UnitIndex[a.Unit]
}

// ParseAttribute takes a string and parses it into an attribute, pulling out
// units if they are specified as a suffix on a number
func ParseAttribute(input string) *Attribute {
	ll := len(input)
	if ll == 0 {
		return &Attribute{String: helper.StringToPtr(input)}
	}

	// Try to parse as a bool
	b, err := strconv.ParseBool(input)
	if err == nil {
		return &Attribute{Bool: helper.BoolToPtr(b)}
	}

	// Check if the string is a number ending with potential units
	if unicode.IsLetter(rune(input[ll-1])) {
		// Try suffix matching
		var unit string
		for _, u := range lengthSortedUnits {
			if strings.HasSuffix(input, u) {
				unit = u
				break
			}
		}

		// Check if we know about the unit. If we don't we can only treat this
		// as a string
		if len(unit) == 0 {
			return &Attribute{String: helper.StringToPtr(input)}
		}

		// Grab the numeric
		numeric := strings.TrimSpace(strings.TrimSuffix(input, unit))

		// Try to parse as an int
		i, err := strconv.ParseInt(numeric, 10, 64)
		if err == nil {
			return &Attribute{Int: helper.Int64ToPtr(i), Unit: unit}
		}

		// Try to parse as a float
		f, err := strconv.ParseFloat(numeric, 64)
		if err == nil {
			return &Attribute{Float: helper.Float64ToPtr(f), Unit: unit}
		}
	}

	// Try to parse as an int
	i, err := strconv.ParseInt(input, 10, 64)
	if err == nil {
		return &Attribute{Int: helper.Int64ToPtr(i)}
	}

	// Try to parse as a float
	f, err := strconv.ParseFloat(input, 64)
	if err == nil {
		return &Attribute{Float: helper.Float64ToPtr(f)}
	}

	return &Attribute{String: helper.StringToPtr(input)}
}
