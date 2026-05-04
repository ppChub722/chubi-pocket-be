package categories

// Seed catalog applied at user registration. See:
// - design/spec/05-categories-tags.md §2.1 (system) + §2.2 (starter)
// - design/spec/05-categories-tags.md §4.14b (color inheritance) + §4.14c
//   (`include_in_report` defaults)
//
// 6 system + 11 expense parents + 37 expense subcategories + 5 income
// flat = 59 starter rows per fresh user. Mirrors the Flutter mock seed
// 1:1 so the API-backed app shows the same Thai-finance taxonomy users
// were piloting against the in-memory mock.
//
// Color hexes are the 12-swatch palette shared by accounts / categories
// / tags. Icon ids are free-form strings (spec §4.12) — clients render
// against their own preset registry.

// ── 6 system categories ────────────────────────────────────────────────

type systemSeed struct {
	Name            string
	Type            string
	Kind            SystemKind
	Icon            string
	Color           string
	IncludeInReport bool
}

// All 6 system categories ship with `include_in_report = false`.
// Spec §03/§2.7: "real activity only" reports — opening balances,
// adjustments, and transfers are bookkeeping, not spending or earning,
// and pollute report totals if counted. The flag is the single rule
// the summary endpoints filter by; this catalog plus the user-flagged
// starter rows (Lending, Reimbursements/Payback) cover every "money
// movement, not real activity" case out of the box.
var systemCatalog = []systemSeed{
	{Name: "Opening Balance", Type: "income", Kind: SystemOpeningIn,
		Icon: "system_opening", Color: "#AED581", IncludeInReport: false},
	{Name: "Opening Balance", Type: "expense", Kind: SystemOpeningOut,
		Icon: "system_opening", Color: "#E57373", IncludeInReport: false},
	{Name: "Adjustment", Type: "income", Kind: SystemAdjustIn,
		Icon: "system_adjustment", Color: "#A1887F", IncludeInReport: false},
	{Name: "Adjustment", Type: "expense", Kind: SystemAdjustOut,
		Icon: "system_adjustment", Color: "#A1887F", IncludeInReport: false},
	{Name: "Transfer In", Type: "income", Kind: SystemTransferIn,
		Icon: "system_transfer", Color: "#4DD0E1", IncludeInReport: false},
	{Name: "Transfer Out", Type: "expense", Kind: SystemTransferOut,
		Icon: "system_transfer", Color: "#4DD0E1", IncludeInReport: false},
	{Name: "Debt Received", Type: "income", Kind: SystemDebtReceived,
		Icon: "system_debt_received", Color: "#AED581", IncludeInReport: false},
	{Name: "Debt Paid", Type: "expense", Kind: SystemDebtPaid,
		Icon: "system_debt_paid", Color: "#E57373", IncludeInReport: false},
}

// ── Starter expense — 11 parents with subs ─────────────────────────────

type starterChild struct {
	Name            string
	Icon            string
	Description     string
	IncludeInReport bool
}

type starterRoot struct {
	Name     string
	Icon     string
	Color    string
	Children []starterChild
}

var starterCatalog = []starterRoot{
	{
		Name: "Food & Drinks", Icon: "restaurant", Color: "#FFB74D",
		Children: []starterChild{
			{Name: "Groceries", Icon: "shopping_basket", IncludeInReport: true,
				Description: "สำหรับของสด, วัตถุดิบ, และน้ำดื่มที่ซื้อมาตุนทำเองที่บ้าน Examples: เนื้อสัตว์, ผัก, เครื่องปรุง, ข้าวสาร, น้ำแพ็ค"},
			{Name: "Food Delivery", Icon: "delivery_dining", IncludeInReport: true,
				Description: "สำหรับยอดรวมบิลจากแอปฯ สั่งอาหารและสินค้าต่างๆ Examples: GrabFood, LINE MAN, ShopeeFood, 7-Delivery"},
			{Name: "Convenience Store", Icon: "convenience_store", IncludeInReport: true,
				Description: "สำหรับของกินและของใช้เล็กน้อยจากร้านสะดวกซื้อ Examples: ขนม, น้ำอัดลม, ไส้กรอก, ของเวฟใน 7-Eleven"},
			{Name: "Buffet", Icon: "restaurant_menu", IncludeInReport: true,
				Description: "สำหรับร้านอาหารแบบบุฟเฟต์ทุกประเภท Examples: ปิ้งย่าง, ชาบู, บุฟเฟต์โรงแรม, ซูชิสายพาน"},
			{Name: "Dining / Take-away", Icon: "restaurant", IncludeInReport: true,
				Description: "สำหรับมื้ออาหารที่ไปนั่งทานที่ร้าน หรือซื้อกลับบ้าน Examples: กินข้าวแกง, ก๋วยเตี๋ยว, อาหารตามสั่ง"},
		},
	},
	{
		Name: "Transportation", Icon: "directions_car", Color: "#64B5F6",
		Children: []starterChild{
			{Name: "Fuel", Icon: "local_gas_station", IncludeInReport: true,
				Description: "ค่าน้ำมันเชื้อเพลิงที่เติมเมื่อมีการเดินทาง Examples: เติมน้ำมันแก๊สโซฮอล์ 95, ดีเซล B7"},
			{Name: "Tolls / Parking", Icon: "local_parking", IncludeInReport: true,
				Description: "ค่าใช้จ่ายที่เกิดขึ้นระหว่างการเดินทางเพื่อความสะดวก/รวดเร็ว Examples: ค่าทางด่วน, ค่าที่จอดรถตามห้าง"},
			{Name: "Public Transit", Icon: "directions_bus", IncludeInReport: true,
				Description: "ค่าเดินทางด้วยระบบขนส่งสาธารณะทุกประเภท Examples: BTS, MRT, รถเมล์, เรือด่วน, วินมอเตอร์ไซค์"},
			{Name: "Taxi / Rideshare", Icon: "local_taxi", IncludeInReport: true,
				Description: "ค่าเดินทางด้วยบริการรถรับจ้างส่วนบุคคลผ่านแอปฯ หรือโบกเรียก Examples: Grab Car, Bolt, แท็กซี่มิเตอร์"},
		},
	},
	{
		Name: "Vehicle", Icon: "car_repair", Color: "#7986CB",
		Children: []starterChild{
			{Name: "Insurance / Tax", Icon: "policy", IncludeInReport: true,
				Description: "ค่าใช้จ่ายรายปีที่จำเป็นเพื่อให้รถใช้งานได้ตามกฎหมาย Examples: ประกันภัยชั้น 1, พ.ร.บ., ภาษีรถยนต์ประจำปี"},
			{Name: "Maintenance", Icon: "car_repair", IncludeInReport: true,
				Description: "ค่าใช้จ่ายในการดูแลรักษารถทั้งเชิงป้องกัน, แก้ไข, และความสะอาด Examples: เข้าศูนย์เช็คระยะ, เปลี่ยนยาง, ซ่อมแอร์, ล้างรถ"},
		},
	},
	{
		Name: "Shopping", Icon: "shopping_bag", Color: "#F06292",
		Children: []starterChild{
			{Name: "Apparel", Icon: "checkroom", IncludeInReport: true,
				Description: "ของใช้ส่วนตัวเพื่อการแต่งกาย เพิ่มบุคลิกภาพ Examples: เสื้อผ้า, กางเกง, รองเท้า, นาฬิกา, กระเป๋า"},
			{Name: "Gadgets / IT", Icon: "headphones", IncludeInReport: true,
				Description: "อุปกรณ์อิเล็กทรอนิกส์พกพา, อุปกรณ์เสริมคอม/มือถือ Examples: หูฟัง, Power Bank, เมาส์, คีย์บอร์ด, สายชาร์จ"},
			{Name: "Home & Electronics", Icon: "tv", IncludeInReport: true,
				Description: "ของใช้และเครื่องใช้ไฟฟ้าชิ้นใหญ่ในบ้าน (คงทน) Examples: ทีวี, ตู้เย็น, ของแต่งบ้าน, เครื่องครัว"},
			{Name: "Personal Care", Icon: "face_retouching", IncludeInReport: true,
				Description: "ของใช้ส่วนตัวที่ใช้แล้วหมดไป (สิ้นเปลือง) Examples: สบู่, ยาสีฟัน, แชมพู, โฟมล้างหน้า, เครื่องสำอาง"},
			{Name: "Household Supplies", Icon: "cleaning_services", IncludeInReport: true,
				Description: "ของใช้ในบ้านที่ใช้แล้วหมดไป (สิ้นเปลือง) Examples: น้ำยาล้างจาน, ผงซักฟอก, ทิชชู่, ถ่านไฟฉาย"},
			{Name: "Hobby / Art", Icon: "palette", IncludeInReport: true,
				Description: "ของที่ใช้สำหรับทำงานอดิเรกหรืองานศิลปะโดยเฉพาะ Examples: สีน้ำ, สมุดสเก็ตช์, ซื้อ Brush/Asset ในแอปวาดรูป"},
		},
	},
	{
		Name: "Bills", Icon: "receipt_long", Color: "#E57373",
		Children: []starterChild{
			{Name: "Housing", Icon: "home", IncludeInReport: true,
				Description: "ค่าใช้จ่ายคงที่และจำเป็นเพื่อให้มีที่อยู่อาศัย Examples: ค่าเช่าห้อง/คอนโด, ค่าน้ำ, ค่าไฟ, ค่าส่วนกลาง"},
			{Name: "Communication", Icon: "wifi", IncludeInReport: true,
				Description: "ค่าใช้จ่ายเพื่อให้สามารถติดต่อสื่อสารและเข้าถึงอินเทอร์เน็ตได้ Examples: ค่าเน็ตบ้าน, ค่าโทรศัพท์มือถือรายเดือน"},
		},
	},
	{
		Name: "Services", Icon: "handshake", Color: "#4DD0E1",
		Children: []starterChild{
			{Name: "Subscriptions", Icon: "subscriptions", IncludeInReport: true,
				Description: "ค่าบริการรายเดือน/ปี เพื่อเข้าถึงเนื้อหาหรือแอปพลิเคชันต่างๆ Examples: Netflix, YouTube Premium, Spotify, iCloud"},
			{Name: "Personal", Icon: "spa", IncludeInReport: true,
				Description: "ค่าบริการที่ทำเพื่อดูแลตัวเองเป็นครั้งคราว Examples: ค่าตัดผม, ทำสปา, นวด, ซักรีด"},
			{Name: "Fitness", Icon: "fitness_center", IncludeInReport: true,
				Description: "ค่าบริการที่เกี่ยวกับการออกกำลังกายและสุขภาพ Examples: ค่าสมาชิกฟิตเนส, คลาสโยคะ"},
		},
	},
	{
		Name: "Health & Medical", Icon: "local_hospital", Color: "#AED581",
		Children: []starterChild{
			{Name: "Doctor / Meds", Icon: "medical_services", IncludeInReport: true,
				Description: "ค่าใช้จ่ายเมื่อเจ็บป่วย หรือซื้อยาเพื่อรักษา/บำรุง Examples: ค่าหาหมอ, ค่ายาตามใบสั่งแพทย์, วิตามิน"},
			{Name: "Vision / Dental", Icon: "visibility", IncludeInReport: true,
				Description: "ค่าใช้จ่ายเกี่ยวกับสายตาและทันตกรรมโดยเฉพาะ Examples: ตัดแว่น, ซื้อคอนแทคเลนส์, ขูดหินปูน, อุดฟัน"},
		},
	},
	{
		Name: "Entertainment", Icon: "movie", Color: "#BA68C8",
		Children: []starterChild{
			{Name: "Gaming", Icon: "sports_esports", IncludeInReport: true,
				Description: "ค่าใช้จ่ายที่เกี่ยวกับการเล่นเกมทั้งหมด Examples: ซื้อเกม (Steam), เติมเงินในเกม, ซื้อเครื่องเกม"},
			{Name: "Media / Books", Icon: "theaters", IncludeInReport: true,
				Description: "ค่าใช้จ่ายเพื่อความบันเทิงผ่านสื่อต่างๆ Examples: ตั๋วหนัง, คอนเสิร์ต, งานอีเวนต์, ซื้อหนังสือ"},
			{Name: "Hangouts", Icon: "nightlife", IncludeInReport: true,
				Description: "ค่าใช้จ่ายเพื่อเข้าสังคมหรือทำกิจกรรมนอกบ้าน Examples: ไปร้านเหล้า, คาเฟ่บอร์ดเกม, คาราโอเกะ"},
			{Name: "Travel", Icon: "flight", IncludeInReport: true,
				Description: "ค่าใช้จ่ายที่เกิดขึ้น \"เพราะ\" การท่องเที่ยวโดยตรง Examples: ค่าตั๋วเครื่องบิน, ค่าโรงแรม, ค่าเช่ารถ"},
		},
	},
	{
		Name: "Investments", Icon: "trending_up", Color: "#AED581",
		Children: []starterChild{
			{Name: "Savings", Icon: "savings", IncludeInReport: true,
				Description: "การโอนเงินไปเก็บในบัญชีเงินออมโดยเฉพาะ Examples: ฝากเงินเข้าบัญชีเงินฝากประจำ, ออมทอง"},
			{Name: "Financial Assets", Icon: "trending_up", IncludeInReport: true,
				Description: "เงินที่ใช้ซื้อสินทรัพย์ทางการเงินทุกประเภทเพื่อหวังผลตอบแทน Examples: ซื้อกองทุนรวม, หุ้น, หุ้นกู้, Cryptocurrency"},
			{Name: "Speculative", Icon: "casino", IncludeInReport: true,
				Description: "การลงทุนในรูปแบบอื่นๆ ที่มีความเสี่ยงเฉพาะตัวหรือไม่เป็นทางการ Examples: ให้เพื่อนยืมเงิน (กินดอกเบี้ย), เล่นแชร์"},
		},
	},
	{
		Name: "Debt Repayments", Icon: "credit_card", Color: "#E57373",
		Children: []starterChild{
			{Name: "Credit Card", Icon: "credit_card", IncludeInReport: true,
				Description: "การจ่ายเพื่อชำระยอดบัตรเครดิต \"ของตัวเราเอง\" Examples: จ่ายบัตรเครดิต KTC, จ่ายบัตร Citi"},
			{Name: "BNPL", Icon: "schedule", IncludeInReport: true,
				Description: "การชำระยอดบริการ \"ซื้อก่อนจ่ายทีหลัง\" Examples: จ่าย SPayLater, จ่าย LazPay, จ่าย Atome"},
			{Name: "Personal Loan", Icon: "handshake", IncludeInReport: true,
				Description: "การชำระหนี้ส่วนตัวคืนให้กับบุคคล Examples: คืนเงินที่ยืมแฟน, คืนเงินที่ยืมคุณแม่"},
		},
	},
	{
		// "Adjustments" sub-category from the original FE taxonomy is
		// intentionally omitted — Adjustment is a system category
		// (sys_adjustment_in / sys_adjustment_out) auto-assigned by the
		// backend on POST /v1/accounts/:id/adjust-balance. Surfacing it
		// here as a user-editable sibling would be a confusing duplicate.
		Name: "Other", Icon: "more_horiz", Color: "#A1887F",
		Children: []starterChild{
			{Name: "Miscellaneous", Icon: "more_horiz", IncludeInReport: true,
				Description: "รายจ่ายเบ็ดเตล็ดทั่วไปที่จำเป็น แต่ไม่เข้าพวกหมวดหมู่อื่น Examples: ค่าธรรมเนียมธนาคาร, ค่าซองจดหมาย, ค่าถ่ายเอกสาร"},
			{Name: "Lending / Pay for Others", Icon: "volunteer_activism", IncludeInReport: false,
				Description: "เงินที่จ่ายแทนคนอื่นไปก่อน หรือให้ยืม Examples: จ่ายค่าข้าวให้เพื่อน, ให้แฟนยืมเงิน"},
		},
	},
}

// ── Starter income — flat (no parents) ─────────────────────────────────

type starterIncome struct {
	Name            string
	Icon            string
	Color           string
	Description     string
	IncludeInReport bool
}

// Income-side "Adjustment" intentionally omitted — same rationale as the
// expense side: sys_adjustment_in covers it.
var starterIncomeCatalog = []starterIncome{
	{Name: "Salary", Icon: "payments", Color: "#AED581", IncludeInReport: true,
		Description: "เงินเดือนจากงานประจำ หรือรายรับหลัก"},
	{Name: "Side Hustle / Freelance", Icon: "work", Color: "#4DD0E1", IncludeInReport: true,
		Description: "รายได้จากงานเสริม, งานฟรีแลนซ์, หรือโปรเจกต์พิเศษ"},
	{Name: "Online Sales", Icon: "storefront", Color: "#FFB74D", IncludeInReport: true,
		Description: "รายได้จากการขายของออนไลน์ทุกประเภท"},
	{Name: "Reimbursements / Payback", Icon: "swap_horiz", Color: "#A1887F", IncludeInReport: false,
		Description: "เงินที่คนอื่นคืนให้ ซึ่งไม่ใช่รายได้ที่แท้จริงของเรา"},
	{Name: "Other Income", Icon: "redeem", Color: "#FFD54F", IncludeInReport: true,
		Description: "รายรับอื่นๆ ที่ไม่เข้าพวก เช่น เงินปันผล, ดอกเบี้ย"},
}
