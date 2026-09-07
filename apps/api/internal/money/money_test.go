package money

import (
	"encoding/json"
	"testing"
)

// The reason the package exists. In float64 this sum is 0.30000000000000004,
// and after enough of them the ledger stops balancing.
func TestAdditionIsExact(t *testing.T) {
	a, b := FromMajor(0.1, "USD"), FromMajor(0.2, "USD")
	sum, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Amount != 30 {
		t.Errorf("expected 30 cents, got %d", sum.Amount)
	}
	if sum.String() != "0.30 USD" {
		t.Errorf("expected 0.30 USD, got %s", sum.String())
	}
}

// Zero-decimal currencies are the ones a hardcoded /100 destroys.
func TestZeroDecimalCurrency(t *testing.T) {
	m := New(50000, "UGX")
	if m.Major() != 50000 {
		t.Errorf("UGX has no minor unit: expected 50000, got %v", m.Major())
	}
	if m.String() != "50000 UGX" {
		t.Errorf("expected 50000 UGX, got %s", m.String())
	}
	if got := FromMajor(50000, "UGX"); got.Amount != 50000 {
		t.Errorf("round trip lost the amount: %d", got.Amount)
	}
}

func TestThreeDecimalCurrency(t *testing.T) {
	m := FromMajor(1.5, "KWD")
	if m.Amount != 1500 {
		t.Errorf("KWD has three decimals: expected 1500 fils, got %d", m.Amount)
	}
}

// Mixing currencies must be refused, not silently added.
func TestCurrencyMismatchIsRefused(t *testing.T) {
	usd, ugx := New(100, "USD"), New(100, "UGX")
	if _, err := usd.Add(ugx); err == nil {
		t.Error("adding USD to UGX must fail")
	}
	if _, err := usd.Sub(ugx); err == nil {
		t.Error("subtracting UGX from USD must fail")
	}
}

// A line total is a unit price times a quantity: exact, no rounding.
func TestMulIntIsExact(t *testing.T) {
	if got := New(1999, "USD").MulInt(3); got.Amount != 5997 {
		t.Errorf("expected 5997, got %d", got.Amount)
	}
}

func TestMulFloatRoundsOnce(t *testing.T) {
	// 19.99 at 7.5% tax is 1.49925, which must land on 150 rather than 149.
	if got := New(1999, "USD").MulFloat(0.075); got.Amount != 150 {
		t.Errorf("expected 150, got %d", got.Amount)
	}
}

// Splitting must not lose or invent a minor unit.
func TestAllocateKeepsEveryUnit(t *testing.T) {
	parts := New(1000, "USD").Allocate(3)
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}
	var total int64
	for _, p := range parts {
		total += p.Amount
	}
	if total != 1000 {
		t.Errorf("allocation lost money: %d of 1000", total)
	}
	if parts[0].Amount != 334 || parts[1].Amount != 333 || parts[2].Amount != 333 {
		t.Errorf("remainder should go to the earliest part: %v", parts)
	}
}

// The wire format carries the currency. A bare number is what this type exists
// to stop, so it is accepted on input for older clients but never emitted.
func TestJSONShape(t *testing.T) {
	out, err := json.Marshal(New(1999, "USD"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"amount":1999,"currency":"USD"}` {
		t.Errorf("unexpected wire format: %s", out)
	}

	var m Money
	if err := json.Unmarshal([]byte(`{"amount":2500,"currency":"UGX"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Amount != 2500 || m.Currency != "UGX" {
		t.Errorf("round trip changed the value: %+v", m)
	}
}

func TestBareNumberIsAcceptedAsMajorUnits(t *testing.T) {
	m := Money{Currency: "USD"}
	if err := json.Unmarshal([]byte("19.99"), &m); err != nil {
		t.Fatal(err)
	}
	if m.Amount != 1999 {
		t.Errorf("a bare 19.99 should read as 1999 cents, got %d", m.Amount)
	}
}

func TestValidateRejectsNonISOCodes(t *testing.T) {
	for _, bad := range []string{"", "US", "usd1", "DOLLAR"} {
		if err := (Money{Currency: bad}).Validate(); err == nil {
			t.Errorf("%q should not validate as a currency", bad)
		}
	}
	if err := (Money{Currency: "USD"}).Validate(); err != nil {
		t.Errorf("USD should validate: %v", err)
	}
}
