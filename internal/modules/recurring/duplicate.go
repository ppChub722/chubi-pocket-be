package recurring

func copyName(n string) string {
	suffix := " (สำเนา)"
	r := []rune(n)
	if len(r)+len([]rune(suffix)) > 100 {
		r = r[:100-len([]rune(suffix))]
	}
	return string(r) + suffix
}
