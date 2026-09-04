package handler

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/base48/member-portal/internal/db"
)

// Bar analytics — the numbers behind /admin/bar.
//
// A note on time, because it is the trap in this table.
//
// The kiosk's sync script (contrib/revbank-sync.sh) sends revbank's own log
// timestamp verbatim — "2026-03-07_04:09:09" — which is *Prague wall clock*.
// parseFlexibleTime parses it with a zoneless layout, so Go labels that wall
// clock as UTC and the driver writes it back as "... +0000 UTC". The stored
// instant is therefore an hour or two earlier than the moment it describes.
//
// Two consequences:
//
//  1. Every stored value is directly usable as wall clock — which is exactly
//     what the buckets here want ("is the bar busy on Friday night?"). Nothing
//     gets converted; only time.Now() is folded into the same space, by
//     barWallNow. Converting the stored values instead (.In(Europe/Prague))
//     would add the offset a second time and move Friday-night sales onto
//     Saturday.
//  2. SQLite's date()/strftime() return NULL on the "+0000 UTC" suffix, so the
//     bucketing cannot live in SQL at all. Hence a full scan aggregated here.
//
// The real fix is at the ingest boundary: parse in Europe/Prague and store
// ISO-8601 UTC, which makes the stored instants true and hands the date
// functions back to SQLite. That needs a migration rewriting the existing rows,
// so it is deliberately not part of this change — see docs/REVBANK_INTEGRATION.md.
//
// The table is a few thousand rows growing by ~600/month, so one full scan per
// page load stays far cheaper than the round trips a SQL version would need.

const (
	// Two purchases by the same person closer together than this are one visit
	// to the fridge, not two. Three hours comfortably spans an evening's
	// re-stocking without merging two separate days.
	barVisitGap = 3 * time.Hour

	// Rolling window for the "recent" half of every comparison.
	barWindow = 30 * 24 * time.Hour

	// A buyer who has not bought anything for this long has drifted off.
	barDormantAfter = 60 * 24 * time.Hour

	// A debt older than this is not a running tab any more, it is a receivable.
	barStaleDebtAfter = 30 * 24 * time.Hour
)

// revbank writes multi-buys as "Fidorka [2x 15.00]" — one row, several units.
// 81 of the first 3648 rows look like this, hiding ~100 units, so unit counts
// are wrong unless the suffix is parsed off.
var barMultiBuyRe = regexp.MustCompile(`^(.*?)\s*\[(\d+)x\s*([0-9.]+)\]$`)

// An "Undo N" row reverses the transaction whose id contains "_TN_".
var barTxNumRe = regexp.MustCompile(`_T(\d+)_`)

type barKind int

const (
	barPurchase barKind = iota
	barDeposit
	barTransfer
	barVoid // an Undo row, or the transaction one reversed
)

type barTx struct {
	Kind    barKind
	User    string
	UserID  sql.NullInt64
	At      time.Time // local
	Amount  int64     // cents, always positive; Kind says which direction
	Product string    // display name, multi-buy suffix stripped
	Key     string    // lowercased Product, for grouping
	Qty     int
	Unit    int64 // cents per unit
}

// --- view models ---

// BarStat is one KPI tile.
type BarStat struct {
	Label    string
	Value    string
	Unit     string
	Note     string
	Delta    float64
	HasDelta bool
	Good     bool // colours the delta: true = up is good
}

// BarColumn is one column of the revenue-over-time chart.
type BarColumn struct {
	Label   string
	Title   string
	Revenue int64
	Count   int
	Buyers  int
	Pct     float64 // height, 0..100
	Alt     bool    // month boundary — a slightly stronger tick
}

// BarMonth is one row of the month table (the chart's table view).
type BarMonth struct {
	Label     string
	Revenue   int64
	Deposits  int64
	Purchases int
	Buyers    int
	Visits    int
	AvgVisit  int64
}

// BarHeatCell is one day×hour cell.
type BarHeatCell struct {
	Hour  int
	Count int
	Step  int // 0 = nothing sold, 1..7 = sequential ramp step
	Title string
}

// BarHeatRow is one weekday of the heatmap.
type BarHeatRow struct {
	Day     string
	Cells   []BarHeatCell
	Count   int
	Revenue int64
	Pct     float64
}

// BarBlockRow is the heatmap's table view: the same day, in four readable
// chunks instead of 24 colour-only cells.
type BarBlockRow struct {
	Day     string
	Blocks  []int
	Total   int
	Revenue int64
}

// BarProduct is one row of the product table.
type BarProduct struct {
	Name      string
	Units     int
	Revenue   int64
	Unit      int64
	Pct       float64 // share of revenue
	BarPct    float64 // relative to the top seller
	Recent    int     // units in the last 30 days
	Delta     float64
	HasDelta  bool
	PriceNote string // set when the unit price moved
}

// BarCategory is one segment of the category bar.
type BarCategory struct {
	Name    string
	Units   int
	Revenue int64
	Pct     float64
	Slot    int // 1..5, fixed per category so filtering never repaints
}

// BarPerson is one row of the people table.
type BarPerson struct {
	Username  string
	UserID    sql.NullInt64
	Revenue   int64
	Purchases int
	Visits    int
	AvgVisit  int64
	Last      time.Time
	DaysSince int
	LastLabel string
	Pct       float64
	BarPct    float64
	Dormant   bool
}

// BarDebtor is one negative balance.
type BarDebtor struct {
	Username  string
	UserID    sql.NullInt64
	Balance   int64 // negative
	Last      time.Time
	HasLast   bool
	DaysSince int
	LastLabel string
	Stale     bool
}

// BarCohortCell is one month of one cohort's retention.
type BarCohortCell struct {
	Future bool
	Count  int
	Pct    float64
	Step   int
	Title  string
}

// BarCohort is one first-purchase month.
type BarCohort struct {
	Label string
	Size  int
	Cells []BarCohortCell
}

// BarAnalytics is everything the dashboard renders.
type BarAnalytics struct {
	HasData bool
	First   time.Time
	Last    time.Time
	Days    int

	Stats []BarStat

	Columns    []BarColumn
	ColumnAxis []string
	Months     []BarMonth

	Heat       []BarHeatRow
	HeatPeak   string
	HeatBlocks []BarBlockRow
	BlockNames []string
	HourLabels []string

	Products    []BarProduct
	ProductsAll int

	Categories []BarCategory

	People    []BarPerson
	PeopleAll int
	Top5Pct   float64
	Top10Pct  float64
	TopN5     int
	TopN10    int

	Debtors    []BarDebtor
	DebtTotal  int64
	CreditHeld int64

	Cohorts      []BarCohort
	CohortMonths []string
}

// barDaysAgo phrases a day count the way Czech does it: "dnes", "včera", and
// otherwise the instrumental "před N dny", which takes the same form for every
// number above one. Formatting "%d dní" would misdeclense every value.
func barDaysAgo(days int) string {
	switch {
	case days <= 0:
		return "dnes"
	case days == 1:
		return "včera"
	default:
		return fmt.Sprintf("před %d dny", days)
	}
}

// barNiceCeil rounds cents up to a round number of CZK — 1, 2, 2.5 or 5 times a
// power of ten — so an axis tick is a number a reader recognises.
func barNiceCeil(cents int64) int64 {
	if cents <= 0 {
		return 0
	}
	czk := float64(cents) / 100
	mag := math.Pow(10, math.Floor(math.Log10(czk)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if czk <= m*mag {
			return int64(m * mag * 100)
		}
	}
	return int64(10 * mag * 100)
}

// barLoc is where the kiosk stands. time.LoadLocation does not cache named
// zones, and this is reached once per rendered table row, so resolve it once.
var barLoc = sync.OnceValue(func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Prague"); err == nil {
		return loc
	}
	return time.Local
})

// barWallNow folds a real instant into the same space the stored timestamps
// live in: Prague wall clock wearing a UTC label. Only then can "now" be
// compared against a stored created_at without an offset creeping in.
func barWallNow(now time.Time) time.Time {
	local := now.In(barLoc())
	y, mo, d := local.Date()
	h, mi, sec := local.Clock()
	return time.Date(y, mo, d, h, mi, sec, 0, time.UTC)
}

// classifyBarTx splits raw rows into the kinds the dashboard counts separately
// and drops the reversed pairs, matching what RevbankSalesStats does in SQL.
func classifyBarTx(rows []db.RevbankTransaction) []barTx {
	// An "Undo N" row reverses the checkout whose id carries "_TN_". Two things
	// make that lookup harder than a map by number:
	//
	//   - one checkout can produce several rows sharing a number (a transfer
	//     writes both the "Give to" and the "Received from" side), and
	//   - revbank restarts reset the counter, so the same number is reused
	//     months apart (T1733 appears on both 23 and 24 May 2026).
	//
	// So resolve an undo to the rows sharing its number at the latest timestamp
	// strictly before the undo itself: same-checkout rows carry an identical
	// timestamp, while a recycled number does not.
	type numbered struct {
		id string
		at time.Time
	}
	byNum := make(map[string][]numbered)
	for _, r := range rows {
		if strings.HasPrefix(r.Description, "Undo ") {
			continue
		}
		if m := barTxNumRe.FindStringSubmatch(r.TransactionID); m != nil {
			byNum[m[1]] = append(byNum[m[1]], numbered{r.TransactionID, r.CreatedAt})
		}
	}

	void := make(map[string]bool)
	for _, r := range rows {
		if !strings.HasPrefix(r.Description, "Undo ") {
			continue
		}
		void[r.TransactionID] = true

		n := strings.TrimSpace(strings.TrimPrefix(r.Description, "Undo "))
		// "At or before", not strictly before: revbank's timestamps have
		// second resolution, so an undo can carry the same one as the checkout
		// it reverses. Requiring "before" would skip it and fall through to an
		// older row wearing the same recycled number.
		var latest time.Time
		for _, c := range byNum[n] {
			if !c.at.After(r.CreatedAt) && c.at.After(latest) {
				latest = c.at
			}
		}
		if latest.IsZero() {
			continue
		}
		for _, c := range byNum[n] {
			if c.at.Equal(latest) {
				void[c.id] = true
			}
		}
	}

	out := make([]barTx, 0, len(rows))
	for _, r := range rows {
		tx := barTx{
			User:   r.Username,
			UserID: r.UserID,
			At:     r.CreatedAt, // already kiosk wall clock; see the file header
			Amount: r.AmountCents,
			Qty:    1,
		}
		if tx.Amount < 0 {
			tx.Amount = -tx.Amount
		}

		switch {
		case void[r.TransactionID]:
			tx.Kind = barVoid
		case strings.HasPrefix(r.Description, "Deposit"):
			tx.Kind = barDeposit
		case strings.HasPrefix(r.Description, "Received from"),
			strings.HasPrefix(r.Description, "Give to"),
			strings.HasPrefix(r.Description, "Sent to"),
			strings.HasPrefix(r.Description, "Reimbursement"):
			tx.Kind = barTransfer
		case r.AmountCents < 0:
			tx.Kind = barPurchase
			tx.Product = strings.TrimSpace(r.Description)
			tx.Unit = tx.Amount
			if m := barMultiBuyRe.FindStringSubmatch(tx.Product); m != nil {
				if q, err := strconv.Atoi(m[2]); err == nil && q > 0 {
					tx.Product = strings.TrimSpace(m[1])
					tx.Qty = q
					// The description states the per-unit price; dividing the
					// total instead truncates ("[3x 8.35]" would give 8.34) and
					// then reads as a price change against the same item sold
					// singly.
					if unit, err := strconv.ParseFloat(m[3], 64); err == nil {
						tx.Unit = int64(math.Round(unit * 100))
					} else {
						tx.Unit = tx.Amount / int64(q)
					}
				}
			}
			tx.Key = strings.ToLower(tx.Product)
		default:
			tx.Kind = barTransfer
		}
		out = append(out, tx)
	}
	return out
}

// barCategories maps products to shelves. Purely a keyword heuristic over
// free-text kiosk names — good enough to see the mix shift, not an inventory.
// First match wins, so order matters: "Birgo mango" is a soft drink, but the
// beer list is checked first and does not contain it.
var barCategories = []struct {
	Name string
	Slot int
	Kw   []string
}{
	{"Pivo a cider", 1, []string{"kozel", "radeck", "radecek", "radek", "birell", "birel", "cider", "plzen", "pivo", "krusovice", "krusvice", "fuchsapfel", "lezak", "ipa", "ale", "staropramen", "bernard", "branik", "mustang", "desitka", "dvanactka", "strix"}},
	{"Energy", 2, []string{"monster", "moster", "semtex", "big shock", "bigshock", "tiger", "hell ", "beast", "bang bang", "rockstar", "energy", "red bull", "redbull", "guarana", "no sleep"}},
	{"Nealko", 3, []string{"cola", "kofola", "orangina", "voda", "ginger", "fanta", "sprite", "juice", "dzus", "limo", "gemerka", "vesna", "ice tea", "mattoni", "birgo", "calipso", "kava", "caj", "g7", "toma", "malina", "citron"}},
	{"Snack", 4, []string{"chips", "cipsy", "bramburky", "bramborky", "horalky", "fidorka", "3bit", "kavenk", "minonk", "suk", "peanut", "arasid", "oriesk", "tycink", "kukurice", "milena", "marilk", "quinos", "noodles", "nudle", "ramyun", "yumyum", "mexicorn", "sedita", "lentilky", "haribo", "bebe", "delissa", "bruschette", "rezy", "venecky", "romanca", "wafer", "leo bar", "gangster", "koko", "kimchi", "solena", "solene", "salt"}},
}

func barCategoryOf(key string) (string, int) {
	for _, c := range barCategories {
		for _, kw := range c.Kw {
			if strings.Contains(key, kw) {
				return c.Name, c.Slot
			}
		}
	}
	return "Ostatní", 5
}

// computeBarAnalytics turns the raw revbank history into the dashboard model.
func computeBarAnalytics(rows []db.RevbankTransaction, accounts []db.RevbankAccount, now time.Time) BarAnalytics {
	now = barWallNow(now)
	a := BarAnalytics{}

	txs := classifyBarTx(rows)
	if len(txs) == 0 {
		return a
	}
	sort.Slice(txs, func(i, j int) bool { return txs[i].At.Before(txs[j].At) })

	a.HasData = true
	a.First = txs[0].At
	a.Last = txs[len(txs)-1].At
	a.Days = int(now.Sub(a.First).Hours()/24) + 1

	winStart := now.Add(-barWindow)
	prevStart := now.Add(-2 * barWindow)

	// --- totals and windows ---
	// Calendar boundaries, in the same wall-clock space as the stored rows. The
	// rolling 30-day window answers "are we trending", these answer "what did we
	// take today" — the question this page gets opened for most.
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonthStart := monthStart.AddDate(0, -1, 0)

	var revTotal, revWin, revPrev, depTotal, depWin int64
	var revToday, revMonth, revLastMonth int64
	var purTotal, purWin int
	buyers := map[string]bool{}
	buyersWin := map[string]bool{}
	for _, t := range txs {
		switch t.Kind {
		case barPurchase:
			revTotal += t.Amount
			purTotal += t.Qty
			buyers[t.User] = true
			if !t.At.Before(dayStart) {
				revToday += t.Amount
			}
			if !t.At.Before(monthStart) {
				revMonth += t.Amount
			} else if !t.At.Before(lastMonthStart) {
				revLastMonth += t.Amount
			}
			if t.At.After(winStart) {
				revWin += t.Amount
				purWin += t.Qty
				buyersWin[t.User] = true
			} else if t.At.After(prevStart) {
				revPrev += t.Amount
			}
		case barDeposit:
			depTotal += t.Amount
			if t.At.After(winStart) {
				depWin += t.Amount
			}
		}
	}

	// --- visits ---
	type visit struct {
		user  string
		start time.Time
		end   time.Time
		spend int64
		items int
	}
	perUser := map[string][]barTx{}
	for _, t := range txs {
		if t.Kind == barPurchase {
			perUser[t.User] = append(perUser[t.User], t)
		}
	}
	var visits []visit
	for user, list := range perUser {
		cur := visit{user: user, start: list[0].At, end: list[0].At, spend: list[0].Amount, items: list[0].Qty}
		for _, t := range list[1:] {
			if t.At.Sub(cur.end) <= barVisitGap {
				cur.end = t.At
				cur.spend += t.Amount
				cur.items += t.Qty
				continue
			}
			visits = append(visits, cur)
			cur = visit{user: user, start: t.At, end: t.At, spend: t.Amount, items: t.Qty}
		}
		visits = append(visits, cur)
	}
	var visitsWin int
	var visitSpendWin int64
	for _, v := range visits {
		if v.start.After(winStart) {
			visitsWin++
			visitSpendWin += v.spend
		}
	}

	// --- balances ---
	var credit, debt int64
	for _, acc := range accounts {
		if acc.BalanceCents > 0 {
			credit += acc.BalanceCents
		} else if acc.BalanceCents < 0 {
			debt += -acc.BalanceCents
		}
	}
	a.CreditHeld = credit
	a.DebtTotal = debt

	// --- KPI tiles ---
	pct := func(cur, prev int64) (float64, bool) {
		if prev <= 0 || a.First.After(prevStart) {
			return 0, false
		}
		return float64(cur-prev) / float64(prev) * 100, true
	}
	d, ok := pct(revWin, revPrev)
	avgVisit := int64(0)
	if visitsWin > 0 {
		avgVisit = visitSpendWin / int64(visitsWin)
	}
	perDay := int64(0)
	if a.Days > 0 {
		perDay = revTotal / int64(a.Days)
	}

	a.Stats = []BarStat{
		{Label: "Tržby dnes", Value: formatCentsAsWholeCZK(revToday), Unit: "Kč",
			Note: "od půlnoci"},
		{Label: "Tržby tento měsíc", Value: formatCentsAsWholeCZK(revMonth), Unit: "Kč",
			Note: fmt.Sprintf("minulý měsíc %s Kč", formatCentsAsWholeCZK(revLastMonth))},
		{Label: "Tržby za 30 dní", Value: formatCentsAsWholeCZK(revWin), Unit: "Kč",
			Note: "proti předchozím 30 dnům", Delta: d, HasDelta: ok, Good: true},
		{Label: "Tržby celkem", Value: formatCentsAsWholeCZK(revTotal), Unit: "Kč",
			Note: fmt.Sprintf("za %d dní provozu, ø %s Kč/den", a.Days, formatCentsAsWholeCZK(perDay))},
		{Label: "Kupujících za 30 dní", Value: formatNumber(int64(len(buyersWin))), Unit: "",
			Note: fmt.Sprintf("%d za celou dobu, %s ks za 30 dní", len(buyers), formatNumber(int64(purWin)))},
		{Label: "ø útrata za návštěvu", Value: formatCentsAsWholeCZK(avgVisit), Unit: "Kč",
			Note: fmt.Sprintf("%s návštěv za 30 dní", formatNumber(int64(visitsWin)))},
		{Label: "Dobito za 30 dní", Value: formatCentsAsWholeCZK(depWin), Unit: "Kč",
			Note: fmt.Sprintf("%s Kč celkem", formatCentsAsWholeCZK(depTotal))},
		{Label: "Kredit na účtech", Value: formatCentsAsWholeCZK(credit), Unit: "Kč",
			Note: "peníze členů, které bar drží"},
		{Label: "Dluh na účtech", Value: formatCentsAsWholeCZK(debt), Unit: "Kč",
			Note: "minusové zůstatky"},
	}

	// --- weekly columns (chart) + monthly rows (its table view) ---
	//
	// Both series are enumerated across the full range rather than read off the
	// bucket maps. A quiet week that produced no rows must still occupy a slot:
	// dropping it silently would put two columns 18 weeks apart side by side
	// under a caption promising consecutive weeks, and would shift every cohort
	// column onto the wrong month.
	type bucket struct {
		rev, dep int64
		count    int
		visits   int
		spend    int64
		users    map[string]bool
	}
	buckets := map[string]*bucket{}
	get := func(k string) *bucket {
		if b, ok := buckets[k]; ok {
			return b
		}
		b := &bucket{users: map[string]bool{}}
		buckets[k] = b
		return b
	}
	mondayOf := func(t time.Time) time.Time {
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, -1)
		}
		return d
	}
	weekKey := func(t time.Time) string { return "w" + mondayOf(t).Format("2006-01-02") }
	monthKey := func(t time.Time) string { return "m" + t.Format("2006-01") }

	for _, t := range txs {
		switch t.Kind {
		case barPurchase:
			w := get(weekKey(t.At))
			w.rev += t.Amount
			w.count += t.Qty
			w.users[t.User] = true

			m := get(monthKey(t.At))
			m.rev += t.Amount
			m.count += t.Qty
			m.users[t.User] = true
		case barDeposit:
			get(monthKey(t.At)).dep += t.Amount
		}
	}
	for _, v := range visits {
		b := get(monthKey(v.start))
		b.visits++
		b.spend += v.spend
	}

	empty := &bucket{users: map[string]bool{}}
	at := func(k string) *bucket {
		if b, ok := buckets[k]; ok {
			return b
		}
		return empty
	}

	var maxRev int64
	for wk := mondayOf(a.First); !wk.After(mondayOf(a.Last)); wk = wk.AddDate(0, 0, 7) {
		if r := at("w" + wk.Format("2006-01-02")).rev; r > maxRev {
			maxRev = r
		}
	}
	// Columns are measured against a round ceiling, so the axis reads
	// 0 / 4 000 / 8 000 rather than 0 / 3 522 / 7 045.
	axisMax := barNiceCeil(maxRev)

	prevMonth := ""
	for wk := mondayOf(a.First); !wk.After(mondayOf(a.Last)); wk = wk.AddDate(0, 0, 7) {
		b := at("w" + wk.Format("2006-01-02"))
		col := BarColumn{
			Label:   wk.Format("2.1."),
			Revenue: b.rev,
			Count:   b.count,
			Buyers:  len(b.users),
			Title: fmt.Sprintf("Týden od %s — %s Kč, %d ks, %d lidí",
				wk.Format("2.1.2006"), formatCentsAsWholeCZK(b.rev), b.count, len(b.users)),
		}
		if axisMax > 0 {
			col.Pct = float64(b.rev) / float64(axisMax) * 100
		}
		if m := wk.Format("2006-01"); m != prevMonth {
			col.Alt = true
			prevMonth = m
		}
		a.Columns = append(a.Columns, col)
	}
	// Three round ticks are enough to read the columns against.
	if axisMax > 0 {
		a.ColumnAxis = []string{
			formatCentsAsWholeCZK(axisMax),
			formatCentsAsWholeCZK(axisMax / 2),
			"0",
		}
	}

	var mKeys []string
	rangeStart := time.Date(a.First.Year(), a.First.Month(), 1, 0, 0, 0, 0, time.UTC)
	rangeEnd := time.Date(a.Last.Year(), a.Last.Month(), 1, 0, 0, 0, 0, time.UTC)
	for mo := rangeStart; !mo.After(rangeEnd); mo = mo.AddDate(0, 1, 0) {
		label := mo.Format("2006-01")
		mKeys = append(mKeys, label)
		b := at("m" + label)
		row := BarMonth{
			Label:     label,
			Revenue:   b.rev,
			Deposits:  b.dep,
			Purchases: b.count,
			Buyers:    len(b.users),
			Visits:    b.visits,
		}
		if b.visits > 0 {
			row.AvgVisit = b.spend / int64(b.visits)
		}
		a.Months = append(a.Months, row)
	}

	// --- heatmap: weekday × hour ---
	var grid [7][24]int
	var gridRev [7][24]int64
	dayIdx := func(t time.Time) int { return (int(t.Weekday()) + 6) % 7 } // Monday = 0
	maxCell, peakDay, peakHour := 0, 0, 0
	var dayCount [7]int
	var dayRev [7]int64
	for _, t := range txs {
		if t.Kind != barPurchase {
			continue
		}
		di, hi := dayIdx(t.At), t.At.Hour()
		grid[di][hi] += t.Qty
		gridRev[di][hi] += t.Amount
		dayCount[di] += t.Qty
		dayRev[di] += t.Amount
		if grid[di][hi] > maxCell {
			maxCell, peakDay, peakHour = grid[di][hi], di, hi
		}
	}
	// The hourly distribution is heavily skewed — one Friday-night cell dwarfs a
	// whole Monday — so a linear ramp collapses nearly every cell onto step 1.
	// Bin by rank instead: equal counts always land on the same step, and all
	// seven steps get used. The caption says the shade is relative.
	var nonZero []int
	for di := 0; di < 7; di++ {
		for hi := 0; hi < 24; hi++ {
			if grid[di][hi] > 0 {
				nonZero = append(nonZero, grid[di][hi])
			}
		}
	}
	sort.Ints(nonZero)
	var thresholds []int
	for i := 1; i <= 6 && len(nonZero) > 0; i++ {
		thresholds = append(thresholds, nonZero[len(nonZero)*i/7])
	}
	heatStep := func(v int) int {
		if v <= 0 {
			return 0
		}
		step := 1
		for _, t := range thresholds {
			if v > t {
				step++
			}
		}
		if step > 7 {
			step = 7
		}
		return step
	}

	dayNames := []string{"Pondělí", "Úterý", "Středa", "Čtvrtek", "Pátek", "Sobota", "Neděle"}
	// The day total is labelled in Kč, so the bar beside it has to be Kč too —
	// scaling it by unit count would let a cheap-snack Saturday outrun a
	// higher-revenue Friday right next to the larger number.
	var maxDayRev int64
	for _, r := range dayRev {
		if r > maxDayRev {
			maxDayRev = r
		}
	}
	for di := 0; di < 7; di++ {
		row := BarHeatRow{Day: dayNames[di], Count: dayCount[di], Revenue: dayRev[di]}
		if maxDayRev > 0 {
			row.Pct = float64(dayRev[di]) / float64(maxDayRev) * 100
		}
		for hi := 0; hi < 24; hi++ {
			cell := BarHeatCell{Hour: hi, Count: grid[di][hi]}
			if cell.Count > 0 {
				cell.Step = heatStep(cell.Count)
				cell.Title = fmt.Sprintf("%s %02d:00 — %d ks, %s Kč",
					dayNames[di], hi, cell.Count, formatCentsAsWholeCZK(gridRev[di][hi]))
			} else {
				cell.Title = fmt.Sprintf("%s %02d:00 — nic", dayNames[di], hi)
			}
			row.Cells = append(row.Cells, cell)
		}
		a.Heat = append(a.Heat, row)

		// Same numbers, four chunks — the readable twin of the colour grid.
		block := BarBlockRow{Day: dayNames[di], Blocks: make([]int, 4),
			Total: dayCount[di], Revenue: dayRev[di]}
		for hi := 0; hi < 24; hi++ {
			block.Blocks[hi/6] += grid[di][hi]
		}
		a.HeatBlocks = append(a.HeatBlocks, block)
	}
	a.BlockNames = []string{"00–06", "06–12", "12–18", "18–24"}
	// Label every third hour; the rest of the columns stay unlabelled so the
	// header does not crowd the 24 cells underneath it.
	a.HourLabels = make([]string, 24)
	for hi := 0; hi < 24; hi += 3 {
		a.HourLabels[hi] = strconv.Itoa(hi)
	}
	if maxCell > 0 {
		a.HeatPeak = fmt.Sprintf("%s kolem %02d:00", dayNames[peakDay], peakHour)
	}

	// --- products ---
	type prod struct {
		name         string
		units        int
		rev          int64
		recent, prev int
		prices       map[int64]int
	}
	pm := map[string]*prod{}
	for _, t := range txs {
		if t.Kind != barPurchase || t.Key == "" {
			continue
		}
		p, ok := pm[t.Key]
		if !ok {
			p = &prod{name: t.Product, prices: map[int64]int{}}
			pm[t.Key] = p
		}
		p.units += t.Qty
		p.rev += t.Amount
		p.prices[t.Unit] += t.Qty
		if t.At.After(winStart) {
			p.recent += t.Qty
		} else if t.At.After(prevStart) {
			p.prev += t.Qty
		}
	}
	var plist []*prod
	for _, p := range pm {
		plist = append(plist, p)
	}
	sort.Slice(plist, func(i, j int) bool {
		if plist[i].rev != plist[j].rev {
			return plist[i].rev > plist[j].rev
		}
		return plist[i].name < plist[j].name
	})
	a.ProductsAll = len(plist)
	top := plist
	if len(top) > 20 {
		top = top[:20]
	}
	var topRev int64
	if len(top) > 0 {
		topRev = top[0].rev
	}
	for _, p := range top {
		row := BarProduct{Name: p.name, Units: p.units, Revenue: p.rev, Recent: p.recent}
		if p.units > 0 {
			row.Unit = p.rev / int64(p.units)
		}
		if revTotal > 0 {
			row.Pct = float64(p.rev) / float64(revTotal) * 100
		}
		if topRev > 0 {
			row.BarPct = float64(p.rev) / float64(topRev) * 100
		}
		// Only claim a trend where both halves have enough units to mean anything.
		if p.prev >= 5 && p.recent >= 1 {
			row.Delta = float64(p.recent-p.prev) / float64(p.prev) * 100
			row.HasDelta = true
		}
		if len(p.prices) > 1 {
			var ps []int64
			for k := range p.prices {
				ps = append(ps, k)
			}
			sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
			row.PriceNote = fmt.Sprintf("%s–%s Kč",
				formatCentsAsWholeCZK(ps[0]), formatCentsAsWholeCZK(ps[len(ps)-1]))
		}
		a.Products = append(a.Products, row)
	}

	// --- categories ---
	cm := map[string]*BarCategory{}
	for _, t := range txs {
		if t.Kind != barPurchase || t.Key == "" {
			continue
		}
		name, slot := barCategoryOf(t.Key)
		c, ok := cm[name]
		if !ok {
			c = &BarCategory{Name: name, Slot: slot}
			cm[name] = c
		}
		c.Units += t.Qty
		c.Revenue += t.Amount
	}
	for _, c := range cm {
		if revTotal > 0 {
			c.Pct = float64(c.Revenue) / float64(revTotal) * 100
		}
		a.Categories = append(a.Categories, *c)
	}
	sort.Slice(a.Categories, func(i, j int) bool { return a.Categories[i].Slot < a.Categories[j].Slot })

	// --- people ---
	type person struct {
		user      string
		id        sql.NullInt64
		rev       int64
		purchases int
		visits    int
		last      time.Time
	}
	people := map[string]*person{}
	for _, t := range txs {
		if t.Kind != barPurchase {
			continue
		}
		p, ok := people[t.User]
		if !ok {
			p = &person{user: t.User, id: t.UserID}
			people[t.User] = p
		}
		if t.UserID.Valid {
			p.id = t.UserID
		}
		p.rev += t.Amount
		p.purchases += t.Qty
		if t.At.After(p.last) {
			p.last = t.At
		}
	}
	for _, v := range visits {
		if p, ok := people[v.user]; ok {
			p.visits++
		}
	}
	var plist2 []*person
	for _, p := range people {
		plist2 = append(plist2, p)
	}
	sort.Slice(plist2, func(i, j int) bool {
		if plist2[i].rev != plist2[j].rev {
			return plist2[i].rev > plist2[j].rev
		}
		return plist2[i].user < plist2[j].user
	})
	a.PeopleAll = len(plist2)
	// Cumulative share of the top N. Taking it at exactly i==4 / i==9 would leave
	// both at zero for a bar with fewer than five buyers, and the template states
	// them unconditionally — so clamp N to what actually exists.
	share := func(n int) float64 {
		if revTotal <= 0 || len(plist2) == 0 {
			return 0
		}
		if n > len(plist2) {
			n = len(plist2)
		}
		var cum int64
		for _, p := range plist2[:n] {
			cum += p.rev
		}
		return float64(cum) / float64(revTotal) * 100
	}
	a.Top5Pct, a.Top10Pct = share(5), share(10)
	a.TopN5, a.TopN10 = min(5, len(plist2)), min(10, len(plist2))
	topPeople := plist2
	if len(topPeople) > 15 {
		topPeople = topPeople[:15]
	}
	var topSpend int64
	if len(topPeople) > 0 {
		topSpend = topPeople[0].rev
	}
	for _, p := range topPeople {
		row := BarPerson{
			Username:  p.user,
			UserID:    p.id,
			Revenue:   p.rev,
			Purchases: p.purchases,
			Visits:    p.visits,
			Last:      p.last,
			DaysSince: int(now.Sub(p.last).Hours() / 24),
			LastLabel: barDaysAgo(int(now.Sub(p.last).Hours() / 24)),
			Dormant:   now.Sub(p.last) > barDormantAfter,
		}
		if p.visits > 0 {
			row.AvgVisit = p.rev / int64(p.visits)
		}
		if revTotal > 0 {
			row.Pct = float64(p.rev) / float64(revTotal) * 100
		}
		if topSpend > 0 {
			row.BarPct = float64(p.rev) / float64(topSpend) * 100
		}
		a.People = append(a.People, row)
	}

	// --- debtors ---
	for _, acc := range accounts {
		if acc.BalanceCents >= 0 {
			continue
		}
		d := BarDebtor{Username: acc.Username, UserID: acc.UserID, Balance: acc.BalanceCents}
		if acc.LastTransactionAt.Valid {
			d.Last = acc.LastTransactionAt.Time // wall clock, like created_at
			d.HasLast = true
			d.DaysSince = int(now.Sub(d.Last).Hours() / 24)
			d.LastLabel = barDaysAgo(d.DaysSince)
			d.Stale = now.Sub(d.Last) > barStaleDebtAfter
		}
		a.Debtors = append(a.Debtors, d)
	}
	sort.Slice(a.Debtors, func(i, j int) bool { return a.Debtors[i].Balance < a.Debtors[j].Balance })

	// --- cohorts ---
	firstMonth := map[string]string{}
	activeIn := map[string]map[string]bool{}
	for _, t := range txs {
		if t.Kind != barPurchase {
			continue
		}
		mk := t.At.Format("2006-01")
		if _, ok := firstMonth[t.User]; !ok {
			firstMonth[t.User] = mk
		}
		if activeIn[mk] == nil {
			activeIn[mk] = map[string]bool{}
		}
		activeIn[mk][t.User] = true
	}
	a.CohortMonths = mKeys
	for _, cm := range mKeys {
		var members []string
		for u, f := range firstMonth {
			if f == cm {
				members = append(members, u)
			}
		}
		if len(members) == 0 {
			continue
		}
		row := BarCohort{Label: cm, Size: len(members)}
		for _, m := range mKeys {
			cell := BarCohortCell{}
			if m < cm {
				cell.Future = true
				row.Cells = append(row.Cells, cell)
				continue
			}
			for _, u := range members {
				if activeIn[m][u] {
					cell.Count++
				}
			}
			cell.Pct = float64(cell.Count) / float64(len(members)) * 100
			// Six steps, not the heatmap's eight: these cells carry a readable
			// number, and steps 4 and 5 of the full ramp clear 4.5:1 against
			// neither dark ink (4.03) nor white (4.42). This ramp skips them.
			cell.Step = 1 + int(cell.Pct/100*4.999)
			if cell.Count == 0 {
				cell.Step = 0
			}
			cell.Title = fmt.Sprintf("%s: %d z %d (%.0f %%) nakoupilo v %s",
				cm, cell.Count, len(members), cell.Pct, m)
			row.Cells = append(row.Cells, cell)
		}
		a.Cohorts = append(a.Cohorts, row)
	}

	return a
}
