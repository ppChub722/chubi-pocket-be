package chatbot

// Every emoji the bot sends lives here (registry agreed 2026-11-02, brief "Card, texts and errors").
const (
	emojiCard     = "🧾"
	emojiWallet   = "👛"
	emojiCategory = "🏷️" // no category, or an icon with no emoji
)

// iconEmoji maps the app's category / wallet icon ids (chubi-pocket-app
// lib/ui/domain/icons/icon_registry.dart) to an emoji. Keep it in step with that file.
var iconEmoji = map[string]string{
	// กระเป๋า / การเงิน
	"wallet": "👛",
	"cash": "💵",
	"bank": "🏦",
	"card": "💳",
	"contactless_card": "💳",
	"credit_card": "💳",
	"e_wallet": "📱",
	"payments": "💵",
	"savings": "🐷",
	"loan": "🧾",
	"receipt_long": "🧾",
	"trending_up": "📈",
	"redeem": "🎟️",
	"gift_card": "🎁",
	"card_giftcard": "🎁",
	// อาหาร
	"restaurant": "🍽️",
	"restaurant_menu": "🍴",
	"shopping_basket": "🧺",
	"delivery_dining": "🛵",
	"convenience_store": "🏪",
	"bakery_dining": "🥐",
	"nightlife": "🍸",
	// เดินทาง
	"directions_car": "🚗",
	"local_gas_station": "⛽",
	"local_parking": "🅿️",
	"directions_bus": "🚌",
	"local_taxi": "🚕",
	"car_repair": "🔧",
	"travel": "✈️",
	"flight": "✈️",
	"policy": "🛡️",
	// ช้อปปิ้ง / ของใช้
	"shopping": "🛍️",
	"shopping_bag": "🛍️",
	"storefront": "🏬",
	"checkroom": "👕",
	"headphones": "🎧",
	"tv": "📺",
	"face_retouching": "💄",
	"cleaning_services": "🧹",
	"palette": "🎨",
	"local_offer": "🏷️",
	"sell": "🏷️",
	// บ้าน / บิล
	"home": "🏠",
	"apartment": "🏢",
	"wifi": "📶",
	"bolt": "⚡",
	"subscriptions": "🔄",
	"schedule": "🗓️",
	"timelapse": "⏱️",
	"notifications": "🔔",
	// สุขภาพ / ไลฟ์สไตล์
	"local_hospital": "🏥",
	"medical_services": "💊",
	"visibility": "👓",
	"spa": "💆",
	"fitness_center": "🏋️",
	"movie": "🎬",
	"theaters": "🎭",
	"sports_esports": "🎮",
	"casino": "🎰",
	"emoji_events": "🏆",
	// งาน / คน
	"business": "💼",
	"work": "💼",
	"work_outline": "💼",
	"engineering": "👷",
	"badge": "🪪",
	"person": "🧑",
	"man": "👨",
	"woman": "👩",
	"face_smile": "🙂",
	"groups": "👥",
	"handshake": "🤝",
	"volunteer_activism": "💝",
	"contact_page": "📇",
	"campaign": "📣",
	// ทั่วไป
	"category": "🏷️",
	"label": "🏷️",
	"more_horiz": "🏷️",
	"tune": "🎛️",
	"flag": "🚩",
	"star": "⭐",
	"favorite": "❤️",
	"folder": "📁",
	"folder_open": "📂",
	"swap_horiz": "↔️",
	// หมวดระบบ (ไม่ส่งให้ model แต่มีไว้ให้ครบ)
	"system_transfer": "↔️",
	"system_adjustment": "⚖️",
	"system_opening": "🏁",
	"system_debt_received": "📥",
	"system_debt_paid": "📤",
}

func categoryEmoji(icon string) string {
	if e, ok := iconEmoji[icon]; ok {
		return e
	}
	return emojiCategory
}
