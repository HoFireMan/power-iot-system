package billingestimate

import (
	"math/big"
	"testing"
)

func rat(value string) *big.Rat {
	result, ok := new(big.Rat).SetString(value)
	if !ok {
		panic(value)
	}
	return result
}

func commercialTiers() []Tier {
	return []Tier{
		{LowerKwh: rat("0"), UpperKwh: rat("330"), RatePerKwh: rat("2.71")},
		{LowerKwh: rat("330"), UpperKwh: rat("700"), RatePerKwh: rat("3.76")},
		{LowerKwh: rat("700"), UpperKwh: rat("1500"), RatePerKwh: rat("4.46")},
		{LowerKwh: rat("1500"), UpperKwh: rat("3000"), RatePerKwh: rat("7.08")},
		{LowerKwh: rat("3000"), UpperKwh: nil, RatePerKwh: rat("7.43")},
	}
}

func TestProgressiveNonTOUAllocatesCommercialThresholdsExactly(t *testing.T) {
	calculator := ProgressiveNonTOUCalculator{}
	for _, test := range []struct {
		name  string
		usage string
		want  []string
	}{
		{name: "zero", usage: "0", want: nil},
		{name: "below first", usage: "329.999999", want: []string{"329.999999"}},
		{name: "first threshold", usage: "330", want: []string{"330"}},
		{name: "just above first", usage: "330.000001", want: []string{"330", "0.000001"}},
		{name: "second threshold", usage: "700", want: []string{"330", "370"}},
		{name: "third threshold", usage: "1500", want: []string{"330", "370", "800"}},
		{name: "just above third", usage: "1500.000001", want: []string{"330", "370", "800", "0.000001"}},
		{name: "fourth threshold", usage: "3000", want: []string{"330", "370", "800", "1500"}},
		{name: "large", usage: "3000.000001", want: []string{"330", "370", "800", "1500", "0.000001"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := calculator.Calculate(rat(test.usage), commercialTiers(), rat("100"))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Tiers) != len(test.want) {
				t.Fatalf("allocations=%d want=%d", len(result.Tiers), len(test.want))
			}
			for index, want := range test.want {
				if result.Tiers[index].UsageKwh.Cmp(rat(want)) != 0 {
					t.Fatalf("tier %d usage=%s want=%s", index, result.Tiers[index].UsageKwh, want)
				}
			}
		})
	}
}

func TestProgressiveNonTOUChargesMinimumAndRoundsExactly(t *testing.T) {
	calculator := ProgressiveNonTOUCalculator{}
	result, err := calculator.Calculate(rat("500"), commercialTiers(), rat("100"))
	if err != nil {
		t.Fatal(err)
	}
	if result.RawEnergyCharge.Cmp(rat("1533.5")) != 0 || result.EnergyCharge.Cmp(rat("1533.5")) != 0 || result.MinimumChargeAdjustment.Sign() != 0 || result.EstimatedTotal.Cmp(rat("1534")) != 0 {
		t.Fatalf("result=%+v", result)
	}
	floor, err := calculator.Calculate(rat("1"), commercialTiers(), rat("100"))
	if err != nil {
		t.Fatal(err)
	}
	if floor.EnergyCharge.Cmp(rat("2.7")) != 0 || floor.MinimumChargeAdjustment.Cmp(rat("97.3")) != 0 || floor.EstimatedTotal.Cmp(rat("100")) != 0 {
		t.Fatalf("floor=%+v", floor)
	}
}

func TestProgressiveNonTOUAllocatesNoncommercialBoundariesForBothPlans(t *testing.T) {
	tiers := []Tier{
		{LowerKwh: rat("0"), UpperKwh: rat("120"), RatePerKwh: rat("1.78")},
		{LowerKwh: rat("120"), UpperKwh: rat("330"), RatePerKwh: rat("2.55")},
		{LowerKwh: rat("330"), UpperKwh: rat("500"), RatePerKwh: rat("3.80")},
		{LowerKwh: rat("500"), UpperKwh: rat("700"), RatePerKwh: rat("5.14")},
		{LowerKwh: rat("700"), UpperKwh: rat("1000"), RatePerKwh: rat("6.44")},
		{LowerKwh: rat("1000"), UpperKwh: nil, RatePerKwh: rat("8.86")},
	}
	calculator := ProgressiveNonTOUCalculator{}
	for _, usage := range []string{"0", "120", "120.000001", "330", "330.000001", "500", "700", "1000", "1000.000001", "5000"} {
		result, err := calculator.Calculate(rat(usage), tiers, rat("100"))
		if err != nil {
			t.Fatalf("usage %s: %v", usage, err)
		}
		var sum big.Rat
		for _, allocation := range result.Tiers {
			sum.Add(&sum, allocation.UsageKwh)
		}
		if sum.Cmp(rat(usage)) != 0 {
			t.Fatalf("usage %s allocated=%s", usage, &sum)
		}
	}
}

func TestProgressiveNonTOURoundingUsesTruncationAndHalfUp(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "1.09", want: "1"},
		{value: "1.099999", want: "1"},
		{value: "1.1", want: "1.1"},
		{value: "1.19", want: "1.1"},
	} {
		if got := FormatDecimal(TruncateToTenth(rat(test.value))); got != test.want {
			t.Fatalf("truncate %s=%s want=%s", test.value, got, test.want)
		}
	}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "1.49", want: "1"},
		{value: "1.5", want: "2"},
		{value: "1.500001", want: "2"},
		{value: "2.49", want: "2"},
	} {
		if got := FormatDecimal(RoundToWhole(rat(test.value))); got != test.want {
			t.Fatalf("round %s=%s want=%s", test.value, got, test.want)
		}
	}
}

func TestProgressiveNonTOURejectsInvalidTiersAndUsage(t *testing.T) {
	calculator := ProgressiveNonTOUCalculator{}
	if _, err := calculator.Calculate(nil, commercialTiers(), rat("100")); err == nil {
		t.Fatal("nil usage accepted")
	}
	if _, err := calculator.Calculate(rat("-1"), commercialTiers(), rat("100")); err == nil {
		t.Fatal("negative usage accepted")
	}
	invalid := commercialTiers()
	invalid[1].LowerKwh = rat("331")
	if _, err := calculator.Calculate(rat("1"), invalid, rat("100")); err == nil {
		t.Fatal("non-contiguous tiers accepted")
	}
}

func TestFormatDecimalPreservesExactDecimalValues(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "0", want: "0"},
		{value: "100.000000", want: "100"},
		{value: "0.000001", want: "0.000001"},
		{value: "1532.300000", want: "1532.3"},
	} {
		if got := FormatDecimal(rat(test.value)); got != test.want {
			t.Fatalf("format %s=%s want=%s", test.value, got, test.want)
		}
	}
}
