package categories

import "strings"

const (
	LangEN = "en"
	LangTH = "th"
)

// Langs lists the languages the starter set is written in. A new language = add its code here and a
// `Lang…: "…"` entry to every Text in the catalog below (a missing entry falls back to English).
var Langs = []string{LangEN, LangTH}

// Text is one string in every language, keyed by language code.
type Text map[string]string

// In returns the text in lang, or English when lang has none.
func (t Text) In(lang string) string {
	if v, ok := t[lang]; ok && v != "" {
		return v
	}
	return t[LangEN]
}

// Is reports whether s is this text in any language (a seeded value the user has not edited).
func (t Text) Is(s string) bool {
	for _, v := range t {
		if v == s {
			return true
		}
	}
	return false
}

// NormalizeLang maps a device / app language ("th", "th-TH", "en_US", "") to a seeded language.
// Anything without a starter set falls back to English.
func NormalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	for _, code := range Langs {
		if l == code || strings.HasPrefix(l, code+"-") || strings.HasPrefix(l, code+"_") {
			return code
		}
	}
	return LangEN
}

type systemSeed struct {
	Name            string
	Type            string
	Kind            SystemKind
	Icon            string
	Color           string
	IncludeInReport bool
}

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
	{Name: "Loan Given", Type: "expense", Kind: SystemLoanGiven,
		Icon: "volunteer_activism", Color: "#E57373", IncludeInReport: false},
	{Name: "Loan Taken", Type: "income", Kind: SystemLoanTaken,
		Icon: "handshake", Color: "#AED581", IncludeInReport: false},
	{Name: "Withdrawal", Type: "expense", Kind: SystemWithdraw,
		Icon: "system_withdraw", Color: "#4DD0E1", IncludeInReport: false},
	{Name: "Deposit", Type: "income", Kind: SystemDeposit,
		Icon: "system_deposit", Color: "#4DD0E1", IncludeInReport: false},
	{Name: "Interest Paid", Type: "expense", Kind: SystemInterestPaid,
		Icon: "percent", Color: "#E57373", IncludeInReport: true},
	{Name: "Interest Received", Type: "income", Kind: SystemInterestReceived,
		Icon: "percent", Color: "#AED581", IncludeInReport: true},
}

// starterChild / starterRoot / starterIncome hold every language; the seed picks one. Key is the
// stable id stored in categories.seed_key (never change one once shipped).
type starterChild struct {
	Key             string
	Name            Text
	Icon            string
	Description     Text
	IncludeInReport bool
}

type starterRoot struct {
	Key      string
	Name     Text
	Icon     string
	Color    string
	Children []starterChild
}

var starterCatalog = []starterRoot{
	{
		Key:  "food_drinks",
		Name: Text{LangEN: "Food & Drinks", LangTH: "อาหารและเครื่องดื่ม"}, Icon: "restaurant", Color: "#FFB74D",
		Children: []starterChild{
			{Key: "food_drinks.groceries", Name: Text{LangEN: "Groceries", LangTH: "ของสดและวัตถุดิบ"}, Icon: "shopping_basket", IncludeInReport: true,
				Description: Text{LangEN: "Fresh food, ingredients and bulk drinks bought to cook at home. Examples: meat, vegetables, seasoning, rice, packed water", LangTH: "สำหรับของสด, วัตถุดิบ, และน้ำดื่มที่ซื้อมาตุนทำเองที่บ้าน Examples: เนื้อสัตว์, ผัก, เครื่องปรุง, ข้าวสาร, น้ำแพ็ค"}},
			{Key: "food_drinks.food_delivery", Name: Text{LangEN: "Food Delivery", LangTH: "สั่งอาหารเดลิเวอรี่"}, Icon: "delivery_dining", IncludeInReport: true,
				Description: Text{LangEN: "Order totals from food and goods delivery apps. Examples: GrabFood, LINE MAN, ShopeeFood, 7-Delivery", LangTH: "สำหรับยอดรวมบิลจากแอปฯ สั่งอาหารและสินค้าต่างๆ Examples: GrabFood, LINE MAN, ShopeeFood, 7-Delivery"}},
			{Key: "food_drinks.convenience_store", Name: Text{LangEN: "Convenience Store", LangTH: "ร้านสะดวกซื้อ"}, Icon: "convenience_store", IncludeInReport: true,
				Description: Text{LangEN: "Small snacks and everyday items from a convenience store. Examples: snacks, soft drinks, sausages, microwave meals at 7-Eleven", LangTH: "สำหรับของกินและของใช้เล็กน้อยจากร้านสะดวกซื้อ Examples: ขนม, น้ำอัดลม, ไส้กรอก, ของเวฟใน 7-Eleven"}},
			{Key: "food_drinks.buffet", Name: Text{LangEN: "Buffet", LangTH: "บุฟเฟต์"}, Icon: "restaurant_menu", IncludeInReport: true,
				Description: Text{LangEN: "All kinds of buffet restaurants. Examples: grill, shabu, hotel buffet, conveyor-belt sushi", LangTH: "สำหรับร้านอาหารแบบบุฟเฟต์ทุกประเภท Examples: ปิ้งย่าง, ชาบู, บุฟเฟต์โรงแรม, ซูชิสายพาน"}},
			{Key: "food_drinks.dining_take_away", Name: Text{LangEN: "Dining / Take-away", LangTH: "ทานที่ร้าน / ซื้อกลับ"}, Icon: "restaurant", IncludeInReport: true,
				Description: Text{LangEN: "Meals eaten at a restaurant or bought to take home. Examples: rice with curry, noodles, made-to-order dishes", LangTH: "สำหรับมื้ออาหารที่ไปนั่งทานที่ร้าน หรือซื้อกลับบ้าน Examples: กินข้าวแกง, ก๋วยเตี๋ยว, อาหารตามสั่ง"}},
		},
	},
	{
		Key:  "transportation",
		Name: Text{LangEN: "Transportation", LangTH: "การเดินทาง"}, Icon: "directions_car", Color: "#64B5F6",
		Children: []starterChild{
			{Key: "transportation.fuel", Name: Text{LangEN: "Fuel", LangTH: "ค่าน้ำมัน"}, Icon: "local_gas_station", IncludeInReport: true,
				Description: Text{LangEN: "Fuel bought for travel. Examples: gasohol 95, diesel B7", LangTH: "ค่าน้ำมันเชื้อเพลิงที่เติมเมื่อมีการเดินทาง Examples: เติมน้ำมันแก๊สโซฮอล์ 95, ดีเซล B7"}},
			{Key: "transportation.tolls_parking", Name: Text{LangEN: "Tolls / Parking", LangTH: "ทางด่วน / ที่จอดรถ"}, Icon: "local_parking", IncludeInReport: true,
				Description: Text{LangEN: "Costs on the road for convenience and speed. Examples: expressway tolls, mall parking", LangTH: "ค่าใช้จ่ายที่เกิดขึ้นระหว่างการเดินทางเพื่อความสะดวก/รวดเร็ว Examples: ค่าทางด่วน, ค่าที่จอดรถตามห้าง"}},
			{Key: "transportation.public_transit", Name: Text{LangEN: "Public Transit", LangTH: "ขนส่งสาธารณะ"}, Icon: "directions_bus", IncludeInReport: true,
				Description: Text{LangEN: "Fares for any public transport. Examples: BTS, MRT, bus, express boat, motorbike taxi", LangTH: "ค่าเดินทางด้วยระบบขนส่งสาธารณะทุกประเภท Examples: BTS, MRT, รถเมล์, เรือด่วน, วินมอเตอร์ไซค์"}},
			{Key: "transportation.taxi_rideshare", Name: Text{LangEN: "Taxi / Rideshare", LangTH: "แท็กซี่ / เรียกรถ"}, Icon: "local_taxi", IncludeInReport: true,
				Description: Text{LangEN: "Private hire rides booked by app or hailed on the street. Examples: Grab Car, Bolt, metered taxi", LangTH: "ค่าเดินทางด้วยบริการรถรับจ้างส่วนบุคคลผ่านแอปฯ หรือโบกเรียก Examples: Grab Car, Bolt, แท็กซี่มิเตอร์"}},
		},
	},
	{
		Key:  "vehicle",
		Name: Text{LangEN: "Vehicle", LangTH: "ยานพาหนะ"}, Icon: "car_repair", Color: "#7986CB",
		Children: []starterChild{
			{Key: "vehicle.insurance_tax", Name: Text{LangEN: "Insurance / Tax", LangTH: "ประกัน / ภาษีรถ"}, Icon: "policy", IncludeInReport: true,
				Description: Text{LangEN: "Yearly costs required to keep the vehicle legal. Examples: class 1 insurance, compulsory insurance, annual road tax", LangTH: "ค่าใช้จ่ายรายปีที่จำเป็นเพื่อให้รถใช้งานได้ตามกฎหมาย Examples: ประกันภัยชั้น 1, พ.ร.บ., ภาษีรถยนต์ประจำปี"}},
			{Key: "vehicle.maintenance", Name: Text{LangEN: "Maintenance", LangTH: "ซ่อมบำรุง"}, Icon: "car_repair", IncludeInReport: true,
				Description: Text{LangEN: "Upkeep of the vehicle: preventive, repairs and cleaning. Examples: scheduled service, new tyres, A/C repair, car wash", LangTH: "ค่าใช้จ่ายในการดูแลรักษารถทั้งเชิงป้องกัน, แก้ไข, และความสะอาด Examples: เข้าศูนย์เช็คระยะ, เปลี่ยนยาง, ซ่อมแอร์, ล้างรถ"}},
		},
	},
	{
		Key:  "shopping",
		Name: Text{LangEN: "Shopping", LangTH: "ช้อปปิ้ง"}, Icon: "shopping_bag", Color: "#F06292",
		Children: []starterChild{
			{Key: "shopping.apparel", Name: Text{LangEN: "Apparel", LangTH: "เสื้อผ้าและเครื่องแต่งกาย"}, Icon: "checkroom", IncludeInReport: true,
				Description: Text{LangEN: "Personal items for dressing up and style. Examples: clothes, trousers, shoes, watches, bags", LangTH: "ของใช้ส่วนตัวเพื่อการแต่งกาย เพิ่มบุคลิกภาพ Examples: เสื้อผ้า, กางเกง, รองเท้า, นาฬิกา, กระเป๋า"}},
			{Key: "shopping.gadgets_it", Name: Text{LangEN: "Gadgets / IT", LangTH: "แกดเจ็ต / ไอที"}, Icon: "headphones", IncludeInReport: true,
				Description: Text{LangEN: "Portable electronics and computer / phone accessories. Examples: headphones, power bank, mouse, keyboard, charging cable", LangTH: "อุปกรณ์อิเล็กทรอนิกส์พกพา, อุปกรณ์เสริมคอม/มือถือ Examples: หูฟัง, Power Bank, เมาส์, คีย์บอร์ด, สายชาร์จ"}},
			{Key: "shopping.home_electronics", Name: Text{LangEN: "Home & Electronics", LangTH: "ของใช้และเครื่องใช้ไฟฟ้าในบ้าน"}, Icon: "tv", IncludeInReport: true,
				Description: Text{LangEN: "Large, durable household goods and appliances. Examples: TV, fridge, home decor, kitchenware", LangTH: "ของใช้และเครื่องใช้ไฟฟ้าชิ้นใหญ่ในบ้าน (คงทน) Examples: ทีวี, ตู้เย็น, ของแต่งบ้าน, เครื่องครัว"}},
			{Key: "shopping.personal_care", Name: Text{LangEN: "Personal Care", LangTH: "ของใช้ส่วนตัว"}, Icon: "face_retouching", IncludeInReport: true,
				Description: Text{LangEN: "Personal items that get used up. Examples: soap, toothpaste, shampoo, face wash, cosmetics", LangTH: "ของใช้ส่วนตัวที่ใช้แล้วหมดไป (สิ้นเปลือง) Examples: สบู่, ยาสีฟัน, แชมพู, โฟมล้างหน้า, เครื่องสำอาง"}},
			{Key: "shopping.household_supplies", Name: Text{LangEN: "Household Supplies", LangTH: "ของใช้ในบ้าน"}, Icon: "cleaning_services", IncludeInReport: true,
				Description: Text{LangEN: "Household items that get used up. Examples: dish soap, detergent, tissue, batteries", LangTH: "ของใช้ในบ้านที่ใช้แล้วหมดไป (สิ้นเปลือง) Examples: น้ำยาล้างจาน, ผงซักฟอก, ทิชชู่, ถ่านไฟฉาย"}},
			{Key: "shopping.hobby_art", Name: Text{LangEN: "Hobby / Art", LangTH: "งานอดิเรก / ศิลปะ"}, Icon: "palette", IncludeInReport: true,
				Description: Text{LangEN: "Things used specifically for hobbies or art. Examples: watercolours, sketchbook, brushes / assets in a drawing app", LangTH: "ของที่ใช้สำหรับทำงานอดิเรกหรืองานศิลปะโดยเฉพาะ Examples: สีน้ำ, สมุดสเก็ตช์, ซื้อ Brush/Asset ในแอปวาดรูป"}},
		},
	},
	{
		Key:  "bills",
		Name: Text{LangEN: "Bills", LangTH: "บิลและค่าสาธารณูปโภค"}, Icon: "receipt_long", Color: "#E57373",
		Children: []starterChild{
			{Key: "bills.housing", Name: Text{LangEN: "Housing", LangTH: "ที่อยู่อาศัย"}, Icon: "home", IncludeInReport: true,
				Description: Text{LangEN: "Fixed, essential costs of having a place to live. Examples: room / condo rent, water, electricity, common fees", LangTH: "ค่าใช้จ่ายคงที่และจำเป็นเพื่อให้มีที่อยู่อาศัย Examples: ค่าเช่าห้อง/คอนโด, ค่าน้ำ, ค่าไฟ, ค่าส่วนกลาง"}},
			{Key: "bills.communication", Name: Text{LangEN: "Communication", LangTH: "โทรศัพท์และอินเทอร์เน็ต"}, Icon: "wifi", IncludeInReport: true,
				Description: Text{LangEN: "Costs of staying in touch and online. Examples: home internet, monthly mobile plan", LangTH: "ค่าใช้จ่ายเพื่อให้สามารถติดต่อสื่อสารและเข้าถึงอินเทอร์เน็ตได้ Examples: ค่าเน็ตบ้าน, ค่าโทรศัพท์มือถือรายเดือน"}},
		},
	},
	{
		Key:  "services",
		Name: Text{LangEN: "Services", LangTH: "บริการ"}, Icon: "handshake", Color: "#4DD0E1",
		Children: []starterChild{
			{Key: "services.subscriptions", Name: Text{LangEN: "Subscriptions", LangTH: "ค่าสมาชิก"}, Icon: "subscriptions", IncludeInReport: true,
				Description: Text{LangEN: "Monthly / yearly fees for content or apps. Examples: Netflix, YouTube Premium, Spotify, iCloud", LangTH: "ค่าบริการรายเดือน/ปี เพื่อเข้าถึงเนื้อหาหรือแอปพลิเคชันต่างๆ Examples: Netflix, YouTube Premium, Spotify, iCloud"}},
			{Key: "services.personal", Name: Text{LangEN: "Personal", LangTH: "บริการส่วนตัว"}, Icon: "spa", IncludeInReport: true,
				Description: Text{LangEN: "Services for looking after yourself now and then. Examples: haircut, spa, massage, laundry", LangTH: "ค่าบริการที่ทำเพื่อดูแลตัวเองเป็นครั้งคราว Examples: ค่าตัดผม, ทำสปา, นวด, ซักรีด"}},
			{Key: "services.fitness", Name: Text{LangEN: "Fitness", LangTH: "ฟิตเนส"}, Icon: "fitness_center", IncludeInReport: true,
				Description: Text{LangEN: "Services for exercise and health. Examples: gym membership, yoga class", LangTH: "ค่าบริการที่เกี่ยวกับการออกกำลังกายและสุขภาพ Examples: ค่าสมาชิกฟิตเนส, คลาสโยคะ"}},
		},
	},
	{
		Key:  "health_medical",
		Name: Text{LangEN: "Health & Medical", LangTH: "สุขภาพและการแพทย์"}, Icon: "local_hospital", Color: "#AED581",
		Children: []starterChild{
			{Key: "health_medical.doctor_meds", Name: Text{LangEN: "Doctor / Meds", LangTH: "หาหมอ / ซื้อยา"}, Icon: "medical_services", IncludeInReport: true,
				Description: Text{LangEN: "Costs when you are ill, or medicine for treatment / supplements. Examples: doctor visit, prescriptions, vitamins", LangTH: "ค่าใช้จ่ายเมื่อเจ็บป่วย หรือซื้อยาเพื่อรักษา/บำรุง Examples: ค่าหาหมอ, ค่ายาตามใบสั่งแพทย์, วิตามิน"}},
			{Key: "health_medical.vision_dental", Name: Text{LangEN: "Vision / Dental", LangTH: "สายตา / ทันตกรรม"}, Icon: "visibility", IncludeInReport: true,
				Description: Text{LangEN: "Costs for eyes and teeth specifically. Examples: glasses, contact lenses, scaling, fillings", LangTH: "ค่าใช้จ่ายเกี่ยวกับสายตาและทันตกรรมโดยเฉพาะ Examples: ตัดแว่น, ซื้อคอนแทคเลนส์, ขูดหินปูน, อุดฟัน"}},
		},
	},
	{
		Key:  "entertainment",
		Name: Text{LangEN: "Entertainment", LangTH: "บันเทิง"}, Icon: "movie", Color: "#BA68C8",
		Children: []starterChild{
			{Key: "entertainment.gaming", Name: Text{LangEN: "Gaming", LangTH: "เกม"}, Icon: "sports_esports", IncludeInReport: true,
				Description: Text{LangEN: "Everything to do with playing games. Examples: buying games (Steam), in-game top-ups, game consoles", LangTH: "ค่าใช้จ่ายที่เกี่ยวกับการเล่นเกมทั้งหมด Examples: ซื้อเกม (Steam), เติมเงินในเกม, ซื้อเครื่องเกม"}},
			{Key: "entertainment.media_books", Name: Text{LangEN: "Media / Books", LangTH: "สื่อบันเทิง / หนังสือ"}, Icon: "theaters", IncludeInReport: true,
				Description: Text{LangEN: "Entertainment through media. Examples: movie tickets, concerts, events, books", LangTH: "ค่าใช้จ่ายเพื่อความบันเทิงผ่านสื่อต่างๆ Examples: ตั๋วหนัง, คอนเสิร์ต, งานอีเวนต์, ซื้อหนังสือ"}},
			{Key: "entertainment.hangouts", Name: Text{LangEN: "Hangouts", LangTH: "สังสรรค์"}, Icon: "nightlife", IncludeInReport: true,
				Description: Text{LangEN: "Going out to socialise or do activities. Examples: bar, board-game cafe, karaoke", LangTH: "ค่าใช้จ่ายเพื่อเข้าสังคมหรือทำกิจกรรมนอกบ้าน Examples: ไปร้านเหล้า, คาเฟ่บอร์ดเกม, คาราโอเกะ"}},
			{Key: "entertainment.travel", Name: Text{LangEN: "Travel", LangTH: "ท่องเที่ยว"}, Icon: "flight", IncludeInReport: true,
				Description: Text{LangEN: "Costs that exist \"because\" of a trip. Examples: flights, hotel, car rental", LangTH: "ค่าใช้จ่ายที่เกิดขึ้น \"เพราะ\" การท่องเที่ยวโดยตรง Examples: ค่าตั๋วเครื่องบิน, ค่าโรงแรม, ค่าเช่ารถ"}},
		},
	},
	{
		Key:  "investments",
		Name: Text{LangEN: "Investments", LangTH: "ออมและลงทุน"}, Icon: "trending_up", Color: "#AED581",
		Children: []starterChild{
			{Key: "investments.savings", Name: Text{LangEN: "Savings", LangTH: "เงินออม"}, Icon: "savings", IncludeInReport: true,
				Description: Text{LangEN: "Money moved into a savings account on purpose. Examples: fixed deposit, buying gold to save", LangTH: "การโอนเงินไปเก็บในบัญชีเงินออมโดยเฉพาะ Examples: ฝากเงินเข้าบัญชีเงินฝากประจำ, ออมทอง"}},
			{Key: "investments.financial_assets", Name: Text{LangEN: "Financial Assets", LangTH: "สินทรัพย์ทางการเงิน"}, Icon: "trending_up", IncludeInReport: true,
				Description: Text{LangEN: "Money used to buy any financial asset for a return. Examples: mutual funds, stocks, bonds, cryptocurrency", LangTH: "เงินที่ใช้ซื้อสินทรัพย์ทางการเงินทุกประเภทเพื่อหวังผลตอบแทน Examples: ซื้อกองทุนรวม, หุ้น, หุ้นกู้, Cryptocurrency"}},
			{Key: "investments.speculative", Name: Text{LangEN: "Speculative", LangTH: "เก็งกำไร"}, Icon: "casino", IncludeInReport: true,
				Description: Text{LangEN: "Other investments with their own risk or informal terms. Examples: lending to a friend for interest, rotating savings group", LangTH: "การลงทุนในรูปแบบอื่นๆ ที่มีความเสี่ยงเฉพาะตัวหรือไม่เป็นทางการ Examples: ให้เพื่อนยืมเงิน (กินดอกเบี้ย), เล่นแชร์"}},
		},
	},
	{
		Key:  "debt_repayments",
		Name: Text{LangEN: "Debt Repayments", LangTH: "ชำระหนี้"}, Icon: "credit_card", Color: "#E57373",
		Children: []starterChild{
			{Key: "debt_repayments.credit_card", Name: Text{LangEN: "Credit Card", LangTH: "บัตรเครดิต"}, Icon: "credit_card", IncludeInReport: true,
				Description: Text{LangEN: "Paying off your own credit card balance. Examples: KTC card payment, Citi card payment", LangTH: "การจ่ายเพื่อชำระยอดบัตรเครดิต \"ของตัวเราเอง\" Examples: จ่ายบัตรเครดิต KTC, จ่ายบัตร Citi"}},
			{Key: "debt_repayments.bnpl", Name: Text{LangEN: "BNPL", LangTH: "ซื้อก่อนจ่ายทีหลัง"}, Icon: "schedule", IncludeInReport: true,
				Description: Text{LangEN: "Paying a \"buy now, pay later\" balance. Examples: SPayLater, LazPay, Atome", LangTH: "การชำระยอดบริการ \"ซื้อก่อนจ่ายทีหลัง\" Examples: จ่าย SPayLater, จ่าย LazPay, จ่าย Atome"}},
			{Key: "debt_repayments.personal_loan", Name: Text{LangEN: "Personal Loan", LangTH: "หนี้ส่วนบุคคล"}, Icon: "handshake", IncludeInReport: true,
				Description: Text{LangEN: "Paying a personal debt back to a person. Examples: repaying a partner, repaying your mother", LangTH: "การชำระหนี้ส่วนตัวคืนให้กับบุคคล Examples: คืนเงินที่ยืมแฟน, คืนเงินที่ยืมคุณแม่"}},
		},
	},
	{
		Key:  "other",
		Name: Text{LangEN: "Other", LangTH: "อื่นๆ"}, Icon: "more_horiz", Color: "#A1887F",
		Children: []starterChild{
			{Key: "other.miscellaneous", Name: Text{LangEN: "Miscellaneous", LangTH: "เบ็ดเตล็ด"}, Icon: "more_horiz", IncludeInReport: true,
				Description: Text{LangEN: "General small necessary expenses that fit no other category. Examples: bank fees, envelopes, photocopies", LangTH: "รายจ่ายเบ็ดเตล็ดทั่วไปที่จำเป็น แต่ไม่เข้าพวกหมวดหมู่อื่น Examples: ค่าธรรมเนียมธนาคาร, ค่าซองจดหมาย, ค่าถ่ายเอกสาร"}},
			{Key: "other.lending_pay_for_others", Name: Text{LangEN: "Lending / Pay for Others", LangTH: "ให้ยืม / จ่ายแทนคนอื่น"}, Icon: "volunteer_activism", IncludeInReport: false,
				Description: Text{LangEN: "Money paid for others first, or lent. Examples: paying a friend's meal, lending to a partner", LangTH: "เงินที่จ่ายแทนคนอื่นไปก่อน หรือให้ยืม Examples: จ่ายค่าข้าวให้เพื่อน, ให้แฟนยืมเงิน"}},
		},
	},
}

type starterIncome struct {
	Key             string
	Name            Text
	Icon            string
	Color           string
	Description     Text
	IncludeInReport bool
}

var starterIncomeCatalog = []starterIncome{
	{Key: "income.salary", Name: Text{LangEN: "Salary", LangTH: "เงินเดือน"}, Icon: "payments", Color: "#AED581", IncludeInReport: true,
		Description: Text{LangEN: "Pay from a regular job, or your main income", LangTH: "เงินเดือนจากงานประจำ หรือรายรับหลัก"}},
	{Key: "income.side_hustle_freelance", Name: Text{LangEN: "Side Hustle / Freelance", LangTH: "งานเสริม / ฟรีแลนซ์"}, Icon: "work", Color: "#4DD0E1", IncludeInReport: true,
		Description: Text{LangEN: "Income from side jobs, freelance work or special projects", LangTH: "รายได้จากงานเสริม, งานฟรีแลนซ์, หรือโปรเจกต์พิเศษ"}},
	{Key: "income.online_sales", Name: Text{LangEN: "Online Sales", LangTH: "ขายของออนไลน์"}, Icon: "storefront", Color: "#FFB74D", IncludeInReport: true,
		Description: Text{LangEN: "Income from selling things online, of any kind", LangTH: "รายได้จากการขายของออนไลน์ทุกประเภท"}},
	{Key: "income.reimbursements_payback", Name: Text{LangEN: "Reimbursements / Payback", LangTH: "เงินคืน / เคลียร์ยอด"}, Icon: "swap_horiz", Color: "#A1887F", IncludeInReport: false,
		Description: Text{LangEN: "Money others paid back to you, which is not real income", LangTH: "เงินที่คนอื่นคืนให้ ซึ่งไม่ใช่รายได้ที่แท้จริงของเรา"}},
	{Key: "income.other_income", Name: Text{LangEN: "Other Income", LangTH: "รายรับอื่นๆ"}, Icon: "redeem", Color: "#FFD54F", IncludeInReport: true,
		Description: Text{LangEN: "Other income that fits nowhere else, such as dividends or interest", LangTH: "รายรับอื่นๆ ที่ไม่เข้าพวก เช่น เงินปันผล, ดอกเบี้ย"}},
}
