// Package locales exposes the list of supported UI/content languages (copy of contracts/locales.json).
package locales

import (
	_ "embed"
	"encoding/json"
	"sort"
)

//go:embed locales.json
var raw []byte

type Locale struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	EnglishName string `json:"english_name"`
	Dir         string `json:"dir"`
}

type file struct {
	Default  string   `json:"default"`
	Fallback string   `json:"fallback"`
	Source   string   `json:"source"`
	Locales  []Locale `json:"locales"`
}

var (
	all      file
	byCode   = map[string]Locale{}
	codesAll []string
)

func init() {
	if err := json.Unmarshal(raw, &all); err != nil {
		panic("locales: " + err.Error())
	}
	for _, l := range all.Locales {
		byCode[l.Code] = l
		codesAll = append(codesAll, l.Code)
	}
	sort.Strings(codesAll)
}

// Known reports whether code is a supported locale.
func Known(code string) bool { _, ok := byCode[code]; return ok }

// Codes returns all locale codes sorted alphabetically.
func Codes() []string { return append([]string(nil), codesAll...) }

// All returns the locale list in file order.
func All() []Locale { return append([]Locale(nil), all.Locales...) }

func Default() string  { return all.Default }
func Fallback() string { return all.Fallback }
func Source() string   { return all.Source }

// EnglishName returns the English name of a locale ("" if unknown).
func EnglishName(code string) string { return byCode[code].EnglishName }

// Dir returns "ltr" or "rtl".
func Dir(code string) string {
	if l, ok := byCode[code]; ok && l.Dir != "" {
		return l.Dir
	}
	return "ltr"
}
