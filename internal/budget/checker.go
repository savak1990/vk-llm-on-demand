package budget

// Config holds every value this package needs; none are hard-coded.
type Config struct {
	MonthlyUSD        float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

type Checker struct {
	cfg Config
}

// New builds a Checker. No clock — every time-sensitive method takes
// now as a parameter from its caller.
func New(cfg Config) *Checker {
	return &Checker{cfg: cfg}
}
