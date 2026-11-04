package categories

import "strings"

const (
	LangEN = "en"
	LangTH = "th"
)

// NormalizeLang maps a device / app language ("th", "th-TH", "en_US", "") to a seeded language.
// Anything without a starter set falls back to English.
func NormalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if l == LangTH || strings.HasPrefix(l, LangTH+"-") || strings.HasPrefix(l, LangTH+"_") {
		return LangTH
	}
	return LangEN
}

func pick(lang, en, th string) string {
	if lang == LangTH {
		return th
	}
	return en
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
}

// starterChild / starterRoot / starterIncome hold both languages; the seed picks one.
type starterChild struct {
	Name            string
	NameTH          string
	Icon            string
	Description     string
	DescriptionTH   string
	IncludeInReport bool
}

type starterRoot struct {
	Name     string
	NameTH   string
	Icon     string
	Color    string
	Children []starterChild
}

var starterCatalog = []starterRoot{
	{
		Name: "Food & Drinks", NameTH: "อาหารและเครื่องดื่ม", Icon: "restaurant", Color: "#FFB74D",
		Children: []starterChild{
			{Name: "Groceries", NameTH: "ของสดและวัตถุดิบ", Icon: "shopping_basket", IncludeInReport: true,
				Description:   "Fresh food, ingredients and bulk drinks bought to cook at home. Examples: meat, vegetables, seasoning, rice, packed water",
				DescriptionTH: "สำหรับของสด, วัตถุดิบ, และน้ำดื่มที่ซื้อมาตุนทำเองที่บ้าน Examples: เนื้อสัตว์, ผัก, เครื่องปรุง, ข้าวสาร, น้ำแพ็ค"},
			{Name: "Food Delivery", NameTH: "สั่งอาหารเดลิเวอรี่", Icon: "delivery_dining", IncludeInReport: true,
				Description:   "Order totals from food and goods delivery apps. Examples: GrabFood, LINE MAN, ShopeeFood, 7-Delivery",
				DescriptionTH: "สำหรับยอดรวมบิลจากแอปฯ สั่งอาหารและสินค้าต่างๆ Examples: GrabFood, LINE MAN, ShopeeFood, 7-Delivery"},
			{Name: "Convenience Store", NameTH: "ร้านสะดวกซื้อ", Icon: "convenience_store", IncludeInReport: true,
				Description:   "Small snacks and everyday items from a convenience store. Examples: snacks, soft drinks, sausages, microwave meals at 7-Eleven",
				DescriptionTH: "สำหรับของกินและของใช้เล็กน้อยจากร้านสะดวกซื้อ Examples: ขนม, น้ำอัดลม, ไส้กรอก, ของเวฟใน 7-Eleven"},
			{Name: "Buffet", NameTH: "บุฟเฟต์", Icon: "restaurant_menu", IncludeInReport: true,
				Description:   "All kinds of buffet restaurants. Examples: grill, shabu, hotel buffet, conveyor-belt sushi",
				DescriptionTH: "สำหรับร้านอาหารแบบบุฟเฟต์ทุกประเภท Examples: ปิ้งย่าง, ชาบู, บุฟเฟต์โรงแรม, ซูชิสายพาน"},
			{Name: "Dining / Take-away", NameTH: "ทานที่ร้าน / ซื้อกลับ", Icon: "restaurant", IncludeInReport: true,
				Description:   "Meals eaten at a restaurant or bought to take home. Examples: rice with curry, noodles, made-to-order dishes",
				DescriptionTH: "สำหรับมื้ออาหารที่ไปนั่งทานที่ร้าน หรือซื้อกลับบ้าน Examples: กินข้าวแกง, ก๋วยเตี๋ยว, อาหารตามสั่ง"},
		},
	},
	{
		Name: "Transportation", NameTH: "การเดินทาง", Icon: "directions_car", Color: "#64B5F6",
		Children: []starterChild{
			{Name: "Fuel", NameTH: "ค่าน้ำมัน", Icon: "local_gas_station", IncludeInReport: true,
				Description:   "Fuel bought for travel. Examples: gasohol 95, diesel B7",
				DescriptionTH: "ค่าน้ำมันเชื้อเพลิงที่เติมเมื่อมีการเดินทาง Examples: เติมน้ำมันแก๊สโซฮอล์ 95, ดีเซล B7"},
			{Name: "Tolls / Parking", NameTH: "ทางด่วน / ที่จอดรถ", Icon: "local_parking", IncludeInReport: true,
				Description:   "Costs on the road for convenience and speed. Examples: expressway tolls, mall parking",
				DescriptionTH: "ค่าใช้จ่ายที่เกิดขึ้นระหว่างการเดินทางเพื่อความสะดวก/รวดเร็ว Examples: ค่าทางด่วน, ค่าที่จอดรถตามห้าง"},
			{Name: "Public Transit", NameTH: "ขนส่งสาธารณะ", Icon: "directions_bus", IncludeInReport: true,
				Description:   "Fares for any public transport. Examples: BTS, MRT, bus, express boat, motorbike taxi",
				DescriptionTH: "ค่าเดินทางด้วยระบบขนส่งสาธารณะทุกประเภท Examples: BTS, MRT, รถเมล์, เรือด่วน, วินมอเตอร์ไซค์"},
			{Name: "Taxi / Rideshare", NameTH: "แท็กซี่ / เรียกรถ", Icon: "local_taxi", IncludeInReport: true,
				Description:   "Private hire rides booked by app or hailed on the street. Examples: Grab Car, Bolt, metered taxi",
				DescriptionTH: "ค่าเดินทางด้วยบริการรถรับจ้างส่วนบุคคลผ่านแอปฯ หรือโบกเรียก Examples: Grab Car, Bolt, แท็กซี่มิเตอร์"},
		},
	},
	{
		Name: "Vehicle", NameTH: "ยานพาหนะ", Icon: "car_repair", Color: "#7986CB",
		Children: []starterChild{
			{Name: "Insurance / Tax", NameTH: "ประกัน / ภาษีรถ", Icon: "policy", IncludeInReport: true,
				Description:   "Yearly costs required to keep the vehicle legal. Examples: class 1 insurance, compulsory insurance, annual road tax",
				DescriptionTH: "ค่าใช้จ่ายรายปีที่จำเป็นเพื่อให้รถใช้งานได้ตามกฎหมาย Examples: ประกันภัยชั้น 1, พ.ร.บ., ภาษีรถยนต์ประจำปี"},
			{Name: "Maintenance", NameTH: "ซ่อมบำรุง", Icon: "car_repair", IncludeInReport: true,
				Description:   "Upkeep of the vehicle: preventive, repairs and cleaning. Examples: scheduled service, new tyres, A/C repair, car wash",
				DescriptionTH: "ค่าใช้จ่ายในการดูแลรักษารถทั้งเชิงป้องกัน, แก้ไข, และความสะอาด Examples: เข้าศูนย์เช็คระยะ, เปลี่ยนยาง, ซ่อมแอร์, ล้างรถ"},
		},
	},
	{
		Name: "Shopping", NameTH: "ช้อปปิ้ง", Icon: "shopping_bag", Color: "#F06292",
		Children: []starterChild{
			{Name: "Apparel", NameTH: "เสื้อผ้าและเครื่องแต่งกาย", Icon: "checkroom", IncludeInReport: true,
				Description:   "Personal items for dressing up and style. Examples: clothes, trousers, shoes, watches, bags",
				DescriptionTH: "ของใช้ส่วนตัวเพื่อการแต่งกาย เพิ่มบุคลิกภาพ Examples: เสื้อผ้า, กางเกง, รองเท้า, นาฬิกา, กระเป๋า"},
			{Name: "Gadgets / IT", NameTH: "แกดเจ็ต / ไอที", Icon: "headphones", IncludeInReport: true,
				Description:   "Portable electronics and computer / phone accessories. Examples: headphones, power bank, mouse, keyboard, charging cable",
				DescriptionTH: "อุปกรณ์อิเล็กทรอนิกส์พกพา, อุปกรณ์เสริมคอม/มือถือ Examples: หูฟัง, Power Bank, เมาส์, คีย์บอร์ด, สายชาร์จ"},
			{Name: "Home & Electronics", NameTH: "ของใช้และเครื่องใช้ไฟฟ้าในบ้าน", Icon: "tv", IncludeInReport: true,
				Description:   "Large, durable household goods and appliances. Examples: TV, fridge, home decor, kitchenware",
				DescriptionTH: "ของใช้และเครื่องใช้ไฟฟ้าชิ้นใหญ่ในบ้าน (คงทน) Examples: ทีวี, ตู้เย็น, ของแต่งบ้าน, เครื่องครัว"},
			{Name: "Personal Care", NameTH: "ของใช้ส่วนตัว", Icon: "face_retouching", IncludeInReport: true,
				Description:   "Personal items that get used up. Examples: soap, toothpaste, shampoo, face wash, cosmetics",
				DescriptionTH: "ของใช้ส่วนตัวที่ใช้แล้วหมดไป (สิ้นเปลือง) Examples: สบู่, ยาสีฟัน, แชมพู, โฟมล้างหน้า, เครื่องสำอาง"},
			{Name: "Household Supplies", NameTH: "ของใช้ในบ้าน", Icon: "cleaning_services", IncludeInReport: true,
				Description:   "Household items that get used up. Examples: dish soap, detergent, tissue, batteries",
				DescriptionTH: "ของใช้ในบ้านที่ใช้แล้วหมดไป (สิ้นเปลือง) Examples: น้ำยาล้างจาน, ผงซักฟอก, ทิชชู่, ถ่านไฟฉาย"},
			{Name: "Hobby / Art", NameTH: "งานอดิเรก / ศิลปะ", Icon: "palette", IncludeInReport: true,
				Description:   "Things used specifically for hobbies or art. Examples: watercolours, sketchbook, brushes / assets in a drawing app",
				DescriptionTH: "ของที่ใช้สำหรับทำงานอดิเรกหรืองานศิลปะโดยเฉพาะ Examples: สีน้ำ, สมุดสเก็ตช์, ซื้อ Brush/Asset ในแอปวาดรูป"},
		},
	},
	{
		Name: "Bills", NameTH: "บิลและค่าสาธารณูปโภค", Icon: "receipt_long", Color: "#E57373",
		Children: []starterChild{
			{Name: "Housing", NameTH: "ที่อยู่อาศัย", Icon: "home", IncludeInReport: true,
				Description:   "Fixed, essential costs of having a place to live. Examples: room / condo rent, water, electricity, common fees",
				DescriptionTH: "ค่าใช้จ่ายคงที่และจำเป็นเพื่อให้มีที่อยู่อาศัย Examples: ค่าเช่าห้อง/คอนโด, ค่าน้ำ, ค่าไฟ, ค่าส่วนกลาง"},
			{Name: "Communication", NameTH: "โทรศัพท์และอินเทอร์เน็ต", Icon: "wifi", IncludeInReport: true,
				Description:   "Costs of staying in touch and online. Examples: home internet, monthly mobile plan",
				DescriptionTH: "ค่าใช้จ่ายเพื่อให้สามารถติดต่อสื่อสารและเข้าถึงอินเทอร์เน็ตได้ Examples: ค่าเน็ตบ้าน, ค่าโทรศัพท์มือถือรายเดือน"},
		},
	},
	{
		Name: "Services", NameTH: "บริการ", Icon: "handshake", Color: "#4DD0E1",
		Children: []starterChild{
			{Name: "Subscriptions", NameTH: "ค่าสมาชิก", Icon: "subscriptions", IncludeInReport: true,
				Description:   "Monthly / yearly fees for content or apps. Examples: Netflix, YouTube Premium, Spotify, iCloud",
				DescriptionTH: "ค่าบริการรายเดือน/ปี เพื่อเข้าถึงเนื้อหาหรือแอปพลิเคชันต่างๆ Examples: Netflix, YouTube Premium, Spotify, iCloud"},
			{Name: "Personal", NameTH: "บริการส่วนตัว", Icon: "spa", IncludeInReport: true,
				Description:   "Services for looking after yourself now and then. Examples: haircut, spa, massage, laundry",
				DescriptionTH: "ค่าบริการที่ทำเพื่อดูแลตัวเองเป็นครั้งคราว Examples: ค่าตัดผม, ทำสปา, นวด, ซักรีด"},
			{Name: "Fitness", NameTH: "ฟิตเนส", Icon: "fitness_center", IncludeInReport: true,
				Description:   "Services for exercise and health. Examples: gym membership, yoga class",
				DescriptionTH: "ค่าบริการที่เกี่ยวกับการออกกำลังกายและสุขภาพ Examples: ค่าสมาชิกฟิตเนส, คลาสโยคะ"},
		},
	},
	{
		Name: "Health & Medical", NameTH: "สุขภาพและการแพทย์", Icon: "local_hospital", Color: "#AED581",
		Children: []starterChild{
			{Name: "Doctor / Meds", NameTH: "หาหมอ / ซื้อยา", Icon: "medical_services", IncludeInReport: true,
				Description:   "Costs when you are ill, or medicine for treatment / supplements. Examples: doctor visit, prescriptions, vitamins",
				DescriptionTH: "ค่าใช้จ่ายเมื่อเจ็บป่วย หรือซื้อยาเพื่อรักษา/บำรุง Examples: ค่าหาหมอ, ค่ายาตามใบสั่งแพทย์, วิตามิน"},
			{Name: "Vision / Dental", NameTH: "สายตา / ทันตกรรม", Icon: "visibility", IncludeInReport: true,
				Description:   "Costs for eyes and teeth specifically. Examples: glasses, contact lenses, scaling, fillings",
				DescriptionTH: "ค่าใช้จ่ายเกี่ยวกับสายตาและทันตกรรมโดยเฉพาะ Examples: ตัดแว่น, ซื้อคอนแทคเลนส์, ขูดหินปูน, อุดฟัน"},
		},
	},
	{
		Name: "Entertainment", NameTH: "บันเทิง", Icon: "movie", Color: "#BA68C8",
		Children: []starterChild{
			{Name: "Gaming", NameTH: "เกม", Icon: "sports_esports", IncludeInReport: true,
				Description:   "Everything to do with playing games. Examples: buying games (Steam), in-game top-ups, game consoles",
				DescriptionTH: "ค่าใช้จ่ายที่เกี่ยวกับการเล่นเกมทั้งหมด Examples: ซื้อเกม (Steam), เติมเงินในเกม, ซื้อเครื่องเกม"},
			{Name: "Media / Books", NameTH: "สื่อบันเทิง / หนังสือ", Icon: "theaters", IncludeInReport: true,
				Description:   "Entertainment through media. Examples: movie tickets, concerts, events, books",
				DescriptionTH: "ค่าใช้จ่ายเพื่อความบันเทิงผ่านสื่อต่างๆ Examples: ตั๋วหนัง, คอนเสิร์ต, งานอีเวนต์, ซื้อหนังสือ"},
			{Name: "Hangouts", NameTH: "สังสรรค์", Icon: "nightlife", IncludeInReport: true,
				Description:   "Going out to socialise or do activities. Examples: bar, board-game cafe, karaoke",
				DescriptionTH: "ค่าใช้จ่ายเพื่อเข้าสังคมหรือทำกิจกรรมนอกบ้าน Examples: ไปร้านเหล้า, คาเฟ่บอร์ดเกม, คาราโอเกะ"},
			{Name: "Travel", NameTH: "ท่องเที่ยว", Icon: "flight", IncludeInReport: true,
				Description:   "Costs that exist \"because\" of a trip. Examples: flights, hotel, car rental",
				DescriptionTH: "ค่าใช้จ่ายที่เกิดขึ้น \"เพราะ\" การท่องเที่ยวโดยตรง Examples: ค่าตั๋วเครื่องบิน, ค่าโรงแรม, ค่าเช่ารถ"},
		},
	},
	{
		Name: "Investments", NameTH: "ออมและลงทุน", Icon: "trending_up", Color: "#AED581",
		Children: []starterChild{
			{Name: "Savings", NameTH: "เงินออม", Icon: "savings", IncludeInReport: true,
				Description:   "Money moved into a savings account on purpose. Examples: fixed deposit, buying gold to save",
				DescriptionTH: "การโอนเงินไปเก็บในบัญชีเงินออมโดยเฉพาะ Examples: ฝากเงินเข้าบัญชีเงินฝากประจำ, ออมทอง"},
			{Name: "Financial Assets", NameTH: "สินทรัพย์ทางการเงิน", Icon: "trending_up", IncludeInReport: true,
				Description:   "Money used to buy any financial asset for a return. Examples: mutual funds, stocks, bonds, cryptocurrency",
				DescriptionTH: "เงินที่ใช้ซื้อสินทรัพย์ทางการเงินทุกประเภทเพื่อหวังผลตอบแทน Examples: ซื้อกองทุนรวม, หุ้น, หุ้นกู้, Cryptocurrency"},
			{Name: "Speculative", NameTH: "เก็งกำไร", Icon: "casino", IncludeInReport: true,
				Description:   "Other investments with their own risk or informal terms. Examples: lending to a friend for interest, rotating savings group",
				DescriptionTH: "การลงทุนในรูปแบบอื่นๆ ที่มีความเสี่ยงเฉพาะตัวหรือไม่เป็นทางการ Examples: ให้เพื่อนยืมเงิน (กินดอกเบี้ย), เล่นแชร์"},
		},
	},
	{
		Name: "Debt Repayments", NameTH: "ชำระหนี้", Icon: "credit_card", Color: "#E57373",
		Children: []starterChild{
			{Name: "Credit Card", NameTH: "บัตรเครดิต", Icon: "credit_card", IncludeInReport: true,
				Description:   "Paying off your own credit card balance. Examples: KTC card payment, Citi card payment",
				DescriptionTH: "การจ่ายเพื่อชำระยอดบัตรเครดิต \"ของตัวเราเอง\" Examples: จ่ายบัตรเครดิต KTC, จ่ายบัตร Citi"},
			{Name: "BNPL", NameTH: "ซื้อก่อนจ่ายทีหลัง", Icon: "schedule", IncludeInReport: true,
				Description:   "Paying a \"buy now, pay later\" balance. Examples: SPayLater, LazPay, Atome",
				DescriptionTH: "การชำระยอดบริการ \"ซื้อก่อนจ่ายทีหลัง\" Examples: จ่าย SPayLater, จ่าย LazPay, จ่าย Atome"},
			{Name: "Personal Loan", NameTH: "หนี้ส่วนบุคคล", Icon: "handshake", IncludeInReport: true,
				Description:   "Paying a personal debt back to a person. Examples: repaying a partner, repaying your mother",
				DescriptionTH: "การชำระหนี้ส่วนตัวคืนให้กับบุคคล Examples: คืนเงินที่ยืมแฟน, คืนเงินที่ยืมคุณแม่"},
		},
	},
	{
		Name: "Other", NameTH: "อื่นๆ", Icon: "more_horiz", Color: "#A1887F",
		Children: []starterChild{
			{Name: "Miscellaneous", NameTH: "เบ็ดเตล็ด", Icon: "more_horiz", IncludeInReport: true,
				Description:   "General small necessary expenses that fit no other category. Examples: bank fees, envelopes, photocopies",
				DescriptionTH: "รายจ่ายเบ็ดเตล็ดทั่วไปที่จำเป็น แต่ไม่เข้าพวกหมวดหมู่อื่น Examples: ค่าธรรมเนียมธนาคาร, ค่าซองจดหมาย, ค่าถ่ายเอกสาร"},
			{Name: "Lending / Pay for Others", NameTH: "ให้ยืม / จ่ายแทนคนอื่น", Icon: "volunteer_activism", IncludeInReport: false,
				Description:   "Money paid for others first, or lent. Examples: paying a friend's meal, lending to a partner",
				DescriptionTH: "เงินที่จ่ายแทนคนอื่นไปก่อน หรือให้ยืม Examples: จ่ายค่าข้าวให้เพื่อน, ให้แฟนยืมเงิน"},
		},
	},
}

type starterIncome struct {
	Name            string
	NameTH          string
	Icon            string
	Color           string
	Description     string
	DescriptionTH   string
	IncludeInReport bool
}

var starterIncomeCatalog = []starterIncome{
	{Name: "Salary", NameTH: "เงินเดือน", Icon: "payments", Color: "#AED581", IncludeInReport: true,
		Description:   "Pay from a regular job, or your main income",
		DescriptionTH: "เงินเดือนจากงานประจำ หรือรายรับหลัก"},
	{Name: "Side Hustle / Freelance", NameTH: "งานเสริม / ฟรีแลนซ์", Icon: "work", Color: "#4DD0E1", IncludeInReport: true,
		Description:   "Income from side jobs, freelance work or special projects",
		DescriptionTH: "รายได้จากงานเสริม, งานฟรีแลนซ์, หรือโปรเจกต์พิเศษ"},
	{Name: "Online Sales", NameTH: "ขายของออนไลน์", Icon: "storefront", Color: "#FFB74D", IncludeInReport: true,
		Description:   "Income from selling things online, of any kind",
		DescriptionTH: "รายได้จากการขายของออนไลน์ทุกประเภท"},
	{Name: "Reimbursements / Payback", NameTH: "เงินคืน / เคลียร์ยอด", Icon: "swap_horiz", Color: "#A1887F", IncludeInReport: false,
		Description:   "Money others paid back to you, which is not real income",
		DescriptionTH: "เงินที่คนอื่นคืนให้ ซึ่งไม่ใช่รายได้ที่แท้จริงของเรา"},
	{Name: "Other Income", NameTH: "รายรับอื่นๆ", Icon: "redeem", Color: "#FFD54F", IncludeInReport: true,
		Description:   "Other income that fits nowhere else, such as dividends or interest",
		DescriptionTH: "รายรับอื่นๆ ที่ไม่เข้าพวก เช่น เงินปันผล, ดอกเบี้ย"},
}
