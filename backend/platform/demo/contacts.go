package demo

type Contact struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	Demo bool   `json:"demo"`
}

func Contacts() []Contact {
	result := []Contact{}
	for _, p := range Profiles() {
		result = append(result, Contact{p.ID, p.Name, p.Role, true})
	}
	return append(result, Contact{"timur", "Тимур Гаранин", "DevOps-дежурный", true}, Contact{"metrics-owner", "Владелец демонстрационных дашбордов", "владелец ресурса", true}, Contact{"library-owner", "Владелец демонстрационной библиотеки", "владелец ресурса", true})
}
