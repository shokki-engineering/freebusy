package quote

import "google.golang.org/genproto/googleapis/type/money"

// Money arithmetic in nanos (1 unit = 1e9 nanos), so the units/nanos split stays
// exact across multiplication, percentages, and sums.

const nanoScale = 1_000_000_000

func nanos(m *money.Money) int64 {
	if m == nil {
		return 0
	}
	return m.GetUnits()*nanoScale + int64(m.GetNanos())
}

func fromNanos(currency string, n int64) *money.Money {
	return &money.Money{CurrencyCode: currency, Units: n / nanoScale, Nanos: int32(n % nanoScale)}
}

func add(a, b *money.Money) *money.Money {
	cur := a.GetCurrencyCode()
	if cur == "" {
		cur = b.GetCurrencyCode()
	}
	return fromNanos(cur, nanos(a)+nanos(b))
}

func sub(a, b *money.Money) *money.Money {
	return fromNanos(a.GetCurrencyCode(), nanos(a)-nanos(b))
}

func mul(m *money.Money, n int64) *money.Money {
	if m == nil {
		return fromNanos("", 0)
	}
	return fromNanos(m.GetCurrencyCode(), nanos(m)*n)
}

func pct(m *money.Money, p int32) *money.Money {
	return fromNanos(m.GetCurrencyCode(), nanos(m)*int64(p)/100)
}

func pctFloat(m *money.Money, p float64) *money.Money {
	return fromNanos(m.GetCurrencyCode(), int64(float64(nanos(m))*p/100.0))
}

func neg(m *money.Money) *money.Money {
	return fromNanos(m.GetCurrencyCode(), -nanos(m))
}

func isZero(m *money.Money) bool { return nanos(m) == 0 }

// IsZero reports whether m is zero-valued (exported for repository callers).
func IsZero(m *money.Money) bool { return isZero(m) }
