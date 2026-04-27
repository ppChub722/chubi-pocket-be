package categories

// Seed catalog applied at user registration. See:
// - design/spec/05-categories-tags.md §2.1 (system) + §2.2 (starter)
// - product/phase1a/db.md §Per-user seed at registration
//
// 6 system categories + 28 starter user categories per user.

type systemSeed struct {
	Name string
	Type string
	Kind SystemKind
}

// systemCatalog is fixed at app boot; the order doesn't matter functionally
// (lookup uses system_kind, not insertion order) but kept stable for clarity.
var systemCatalog = []systemSeed{
	{Name: "Opening Balance", Type: "income", Kind: SystemOpeningIn},
	{Name: "Opening Balance", Type: "expense", Kind: SystemOpeningOut},
	{Name: "Adjustment", Type: "income", Kind: SystemAdjustIn},
	{Name: "Adjustment", Type: "expense", Kind: SystemAdjustOut},
	{Name: "Transfer In", Type: "income", Kind: SystemTransferIn},
	{Name: "Transfer Out", Type: "expense", Kind: SystemTransferOut},
}

type starterSeed struct {
	Name     string
	Type     string
	Children []string // immediate children, all under this parent
}

// starterCatalog: 13 expense roots + 6 income roots = 19 roots; 15 children
// under expense roots; total 34 starter user rows. Matches spec §2.2.
var starterCatalog = []starterSeed{
	{Name: "Food", Type: "expense", Children: []string{"Restaurants", "Groceries", "Coffee & Drinks"}},
	{Name: "Transport", Type: "expense", Children: []string{"Taxi / Grab", "Public Transit", "Fuel"}},
	{Name: "Bills & Utilities", Type: "expense", Children: []string{"Electricity", "Water", "Internet", "Phone"}},
	{Name: "Shopping", Type: "expense", Children: []string{"Clothing", "Electronics"}},
	{Name: "Health", Type: "expense", Children: []string{"Medical", "Pharmacy"}},
	{Name: "Entertainment", Type: "expense", Children: []string{"Movies", "Subscriptions"}},
	{Name: "Personal Care", Type: "expense"},
	{Name: "Education", Type: "expense"},
	{Name: "Gifts & Donations", Type: "expense"},
	{Name: "Travel", Type: "expense"},
	{Name: "Others", Type: "expense"},

	{Name: "Salary", Type: "income"},
	{Name: "Freelance", Type: "income"},
	{Name: "Interest", Type: "income"},
	{Name: "Refund", Type: "income"},
	{Name: "Gift", Type: "income"},
	{Name: "Others", Type: "income"},
}
