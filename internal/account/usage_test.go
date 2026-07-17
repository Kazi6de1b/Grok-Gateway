package account

import "testing"

func TestParseUsageCreditsPayload(t *testing.T) {
	value, err := ParseUsage([]byte(`{"subscription":{"name":"Super Plan"},"config":{"creditUsagePercent":42.5,"currentPeriod":{"start":"2026-07-08T00:00:00Z","end":"2026-07-15T00:00:00Z"},"onDemandCap":{"val":50},"onDemandUsed":{"val":12.5}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.PlanName != "Super Plan" || value.UsagePercent != 42.5 || value.OnDemandCap != 50 || value.OnDemandUsed != 12.5 || value.UsagePeriodEnd != "2026-07-15T00:00:00Z" {
		t.Fatalf("unexpected usage: %#v", value)
	}
}

func TestParseUsageMonthlyPayload(t *testing.T) {
	value, err := ParseUsage([]byte(`{"config":{"monthlyLimit":{"val":100},"used":{"val":25},"billingPeriodEnd":"2026-08-01T00:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.MonthlyLimit != 100 || value.Used != 25 || value.UsagePercent != 25 {
		t.Fatalf("unexpected usage: %#v", value)
	}
}
