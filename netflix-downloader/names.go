package netflix

import "strings"

// Human-readable names are persisted alongside the original BCP 47 tag.
func Name(tag string) string {
	exact := map[string]string{"es-ES": "Español (España)", "es-419": "Español (Latinoamérica)", "es-MX": "Español (México)", "pt-BR": "Portugués (Brasil)", "pt-PT": "Portugués (Portugal)", "en-US": "Inglés (Estados Unidos)", "en-GB": "Inglés (Reino Unido)", "zh-Hans": "Chino simplificado", "zh-Hant": "Chino tradicional"}
	if name := exact[tag]; name != "" {
		return name
	}
	base := strings.ToLower(strings.Split(tag, "-")[0])
	name := map[string]string{"en": "Inglés", "es": "Español", "fr": "Francés", "de": "Alemán", "it": "Italiano", "pt": "Portugués", "ja": "Japonés", "ko": "Coreano", "zh": "Chino", "ar": "Árabe", "hi": "Hindi", "tr": "Turco", "ru": "Ruso", "pl": "Polaco", "nl": "Neerlandés", "sv": "Sueco", "da": "Danés", "no": "Noruego", "fi": "Finés", "cs": "Checo", "hu": "Húngaro", "ro": "Rumano", "th": "Tailandés", "ta": "Tamil", "te": "Telugu", "id": "Indonesio", "vi": "Vietnamita", "uk": "Ucraniano", "he": "Hebreo"}[base]
	if name == "" {
		return tag
	}
	if strings.Contains(tag, "-") {
		return name + " (" + tag + ")"
	}
	return name
}
